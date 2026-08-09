package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/voocel/mcp-sdk-go/protocol"
	"github.com/voocel/mcp-sdk-go/server"
	"github.com/voocel/mcp-sdk-go/transport/stdio"
)

const (
	legacyMCPProtocolVersion = "2024-11-05"
	mcpMaxMessageBytes       = 16 << 20
)

// serveMCP keeps the current stateless protocol as the primary wire contract
// while translating the legacy initialize handshake used by clients such as
// Jcode into current per-request metadata for the SDK server.
func serveMCP(ctx context.Context, srv *server.Server, version string, reader io.Reader, writer io.Writer) error {
	if reader == nil {
		reader = os.Stdin
	}
	if writer == nil {
		writer = os.Stdout
	}

	inputReader, inputWriter := io.Pipe()
	output := &mcpLockedWriter{writer: writer}
	rewriteCtx, cancelRewrite := context.WithCancel(ctx)
	defer cancelRewrite()
	rewriteDone := make(chan error, 1)
	go func() {
		err := rewriteMCPInput(rewriteCtx, reader, inputWriter, output, version)
		// Publish the result before closing the pipe so a normal EOF cannot race
		// serveMCP's non-blocking shutdown check.
		rewriteDone <- err
		if err != nil {
			_ = inputWriter.CloseWithError(err)
		} else {
			_ = inputWriter.Close()
		}
	}()

	serveErr := stdio.Serve(ctx, srv, &stdio.Options{
		Reader: inputReader,
		Writer: output,
	})
	// Unblock the rewriter if the SDK transport stopped reading because the
	// context was cancelled or the output failed. An arbitrary io.Reader may
	// not be interruptible, so shutdown must not wait for it indefinitely.
	cancelRewrite()
	_ = inputReader.Close()
	_ = inputWriter.Close()
	rewriteErr, rewriteFinished := pollMCPRewrite(rewriteDone)
	if !rewriteFinished {
		if closer, ok := reader.(io.Closer); ok {
			_ = closer.Close()
		}
		rewriteErr, rewriteFinished = pollMCPRewrite(rewriteDone)
	}
	if serveErr != nil {
		return serveErr
	}
	if rewriteFinished && rewriteErr != nil && !errors.Is(rewriteErr, context.Canceled) {
		return rewriteErr
	}
	return nil
}

func pollMCPRewrite(done <-chan error) (error, bool) {
	select {
	case err := <-done:
		return err, true
	default:
		return nil, false
	}
}

type mcpLockedWriter struct {
	mu     sync.Mutex
	writer io.Writer
}

func (w *mcpLockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(p)
}

type mcpCompatRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// runPipedInput preserves the zero-argument one-shot JSON protocol while also
// supporting MCP hosts that can configure only an executable path. The first
// JSON value is replayed unchanged after selecting the protocol.
func runPipedInput(ctx context.Context, reader io.Reader, writer io.Writer) error {
	var captured bytes.Buffer
	limited := &io.LimitedReader{R: reader, N: mcpMaxMessageBytes + 1}
	decoder := json.NewDecoder(io.TeeReader(limited, &captured))
	var envelope mcpCompatRequest
	decodeErr := decoder.Decode(&envelope)
	replay := io.MultiReader(bytes.NewReader(captured.Bytes()), reader)
	if decodeErr == nil && envelope.JSONRPC == "2.0" && envelope.Method != "" {
		return runMCPWithIO(ctx, nil, replay, writer)
	}
	return runProtocol(ctx, replay, writer)
}

func rewriteMCPInput(ctx context.Context, reader io.Reader, pipeWriter *io.PipeWriter, output io.Writer, version string) error {
	br := bufio.NewReaderSize(reader, 64<<10)
	legacy := false
	for {
		line, err := readMCPCompatLine(br, mcpMaxMessageBytes)
		if len(line) > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			request, decodeErr := decodeMCPCompatRequest(line)
			if decodeErr == nil {
				switch request.Method {
				case "initialize":
					legacy = true
					if len(request.ID) > 0 && !bytes.Equal(bytes.TrimSpace(request.ID), []byte("null")) {
						if err := writeMCPCompatResponse(output, request.ID, map[string]any{
							"protocolVersion": legacyMCPProtocolVersion,
							"capabilities":    map[string]any{"tools": map[string]any{}},
							"serverInfo": protocol.Implementation{
								Name:    "webfetch",
								Title:   "webfetch",
								Version: versionOrDev(version),
							},
							"instructions": "Use web_search for public web search and web_fetch to retrieve a URL as clean Markdown.",
						}); err != nil {
							return err
						}
					}
					continue
				case "notifications/initialized":
					if legacy {
						continue
					}
				case "shutdown":
					if legacy {
						if len(request.ID) > 0 && !bytes.Equal(bytes.TrimSpace(request.ID), []byte("null")) {
							if err := writeMCPCompatResponse(output, request.ID, map[string]any{}); err != nil {
								return err
							}
						}
						continue
					}
				}

				if legacy && request.Method != "" && !hasCurrentMCPMetadata(request.Params) {
					transformed, transformErr := addCurrentMCPMetadata(line, request.Params)
					if transformErr == nil {
						line = transformed
					}
				}
			}

			if _, err := pipeWriter.Write(append(line, '\n')); err != nil {
				return err
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func decodeMCPCompatRequest(line []byte) (mcpCompatRequest, error) {
	var request mcpCompatRequest
	if err := json.Unmarshal(line, &request); err != nil {
		return mcpCompatRequest{}, err
	}
	return request, nil
}

func hasCurrentMCPMetadata(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var params map[string]json.RawMessage
	if err := json.Unmarshal(raw, &params); err != nil || params == nil {
		return false
	}
	var meta map[string]json.RawMessage
	if err := json.Unmarshal(params["_meta"], &meta); err != nil || meta == nil {
		return false
	}
	_, hasVersion := meta[protocol.MetaProtocolVersion]
	_, hasCapabilities := meta[protocol.MetaClientCapabilities]
	return hasVersion && hasCapabilities
}

func addCurrentMCPMetadata(line []byte, paramsRaw json.RawMessage) ([]byte, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(line, &envelope); err != nil {
		return nil, err
	}
	params := make(map[string]json.RawMessage)
	if len(paramsRaw) > 0 && !bytes.Equal(bytes.TrimSpace(paramsRaw), []byte("null")) {
		if err := json.Unmarshal(paramsRaw, &params); err != nil {
			return nil, err
		}
		if params == nil {
			params = make(map[string]json.RawMessage)
		}
	}
	meta, err := json.Marshal(map[string]any{
		protocol.MetaProtocolVersion:    protocol.Version,
		protocol.MetaClientInfo:         protocol.Implementation{Name: "legacy-mcp-client", Version: legacyMCPProtocolVersion},
		protocol.MetaClientCapabilities: map[string]any{},
	})
	if err != nil {
		return nil, err
	}
	params["_meta"] = meta
	envelope["params"], err = json.Marshal(params)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope)
}

func writeMCPCompatResponse(writer io.Writer, id json.RawMessage, result any) error {
	response, err := json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  any             `json:"result"`
	}{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	})
	if err != nil {
		return fmt.Errorf("encode MCP compatibility response: %w", err)
	}
	_, err = writer.Write(append(response, '\n'))
	return err
}

func readMCPCompatLine(br *bufio.Reader, max int64) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := br.ReadSlice('\n')
		buf = append(buf, chunk...)
		if int64(len(buf)) > max {
			return nil, fmt.Errorf("stdio: message exceeds %d bytes", max)
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		return bytes.TrimRight(buf, "\r\n"), err
	}
}

func versionOrDev(version string) string {
	if version == "" {
		return "dev"
	}
	return version
}
