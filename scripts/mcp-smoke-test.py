#!/usr/bin/env python3
"""Black-box smoke tests for the current and legacy-compatible webfetch MCP stdio server."""

from __future__ import annotations

import argparse
import json
import os
import select
import signal
import socket
import subprocess
import sys
import threading
import time
from typing import Any
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen


PROTOCOL_VERSION = "2026-07-28"
LEGACY_PROTOCOL_VERSION = "2024-11-05"
DEFAULT_TIMEOUT = 3.0


class SmokeFailure(AssertionError):
    """A required smoke-test case failed."""


def check(condition: bool, message: str) -> None:
    if not condition:
        raise SmokeFailure(message)


class MCPProcess:
    def __init__(self, binary: str, timeout: float, compact: bool = False, bare: bool = False) -> None:
        self.binary = binary
        self.timeout = timeout
        command = [binary] if bare else [binary, "--mcp"]
        if compact and not bare:
            command.append("--compact")
        process_env = os.environ.copy()
        process_env["WEBFETCH_MCP_LOG_LEVEL"] = "off"
        self.proc = subprocess.Popen(
            command,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            env=process_env,
        )
        assert self.proc.stdin is not None
        assert self.proc.stdout is not None
        assert self.proc.stderr is not None
        self.stdin = self.proc.stdin
        self.stdout = self.proc.stdout
        self.stderr = self.proc.stderr
        self._buffer = bytearray()
        self.stdout_lines: list[str] = []
        self.stderr_bytes = bytearray()
        self._stderr_thread = threading.Thread(target=self._drain_stderr, daemon=True)
        self._stderr_thread.start()

    def _drain_stderr(self) -> None:
        while True:
            chunk = self.stderr.read(4096)
            if not chunk:
                return
            self.stderr_bytes.extend(chunk)

    def send_raw(self, line: str) -> None:
        self.stdin.write(line.encode("utf-8") + b"\n")
        self.stdin.flush()

    def send(self, message: dict[str, Any]) -> str:
        encoded = json.dumps(message, separators=(",", ":"), sort_keys=True)
        self.send_raw(encoded)
        return encoded

    def recv(self) -> dict[str, Any]:
        deadline = time.monotonic() + self.timeout
        fd = self.stdout.fileno()
        while b"\n" not in self._buffer:
            remaining = deadline - time.monotonic()
            check(remaining > 0, f"timed out waiting for MCP response; stdout={self.stdout_lines!r}")
            ready, _, _ = select.select([fd], [], [], remaining)
            check(ready, f"timed out waiting for MCP response; stdout={self.stdout_lines!r}")
            chunk = os.read(fd, 4096)
            check(chunk, "MCP process closed stdout before returning a response")
            self._buffer.extend(chunk)

        raw_line, _, self._buffer = self._buffer.partition(b"\n")

        line = raw_line.decode("utf-8", errors="replace")
        self.stdout_lines.append(line)
        try:
            value = json.loads(line)
        except json.JSONDecodeError as exc:
            raise SmokeFailure(f"stdout line is not JSON: {line!r}: {exc}") from exc
        check(isinstance(value, dict), f"MCP response is not a JSON object: {value!r}")
        check(value.get("jsonrpc") == "2.0", f"invalid jsonrpc response: {value!r}")
        return value

    def close(self) -> tuple[int, str, str]:
        if self.stdin.closed is False:
            self.stdin.close()
        try:
            return_code = self.proc.wait(timeout=self.timeout)
        except subprocess.TimeoutExpired as exc:
            self.proc.kill()
            self.proc.wait()
            raise SmokeFailure("MCP process did not exit after stdin EOF") from exc
        self._stderr_thread.join(timeout=self.timeout)
        remaining = bytes(self._buffer) + self.stdout.read()
        self._buffer.clear()
        if remaining:
            for line in remaining.decode("utf-8", errors="replace").splitlines():
                if line.strip():
                    self.stdout_lines.append(line)
                    try:
                        value = json.loads(line)
                    except json.JSONDecodeError as exc:
                        raise SmokeFailure(f"stdout line is not JSON after EOF: {line!r}") from exc
                    check(value.get("jsonrpc") == "2.0", f"invalid jsonrpc response after EOF: {value!r}")
        return return_code, bytes(self.stderr_bytes).decode("utf-8", errors="replace"), "\n".join(self.stdout_lines)

    def interrupt(self) -> tuple[int, str]:
        self.proc.send_signal(signal.SIGINT)
        try:
            return_code = self.proc.wait(timeout=self.timeout)
        except subprocess.TimeoutExpired as exc:
            self.proc.kill()
            self.proc.wait()
            raise SmokeFailure("MCP process did not exit after SIGINT while stdin remained open") from exc
        try:
            self.stdin.close()
        except BrokenPipeError:
            pass
        self._stderr_thread.join(timeout=self.timeout)
        return return_code, bytes(self.stderr_bytes).decode("utf-8", errors="replace")

    def abort(self) -> None:
        if self.proc.poll() is None:
            self.proc.kill()
            self.proc.wait()
        self._stderr_thread.join(timeout=1)


def metadata() -> dict[str, Any]:
    return {
        "io.modelcontextprotocol/protocolVersion": PROTOCOL_VERSION,
        "io.modelcontextprotocol/clientInfo": {
            "name": "webfetch-smoke-test",
            "version": "test",
        },
        "io.modelcontextprotocol/clientCapabilities": {},
    }


def request(request_id: int, method: str, params: dict[str, Any] | None = None) -> dict[str, Any]:
    merged = dict(params or {})
    merged["_meta"] = metadata()
    return {"jsonrpc": "2.0", "id": request_id, "method": method, "params": merged}


def error_code(response: dict[str, Any]) -> int | None:
    error = response.get("error")
    if not isinstance(error, dict):
        return None
    code = error.get("code")
    return code if isinstance(code, int) else None


def result_text(response: dict[str, Any]) -> str:
    result = response.get("result")
    if not isinstance(result, dict):
        return ""
    chunks = []
    for content in result.get("content", []):
        if isinstance(content, dict) and content.get("type") == "text":
            text = content.get("text")
            if isinstance(text, str):
                chunks.append(text)
    return "\n".join(chunks)


def run_command(binary: str, *args: str) -> subprocess.CompletedProcess[bytes]:
    try:
        return subprocess.run(
            [binary, *args],
            check=False,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=DEFAULT_TIMEOUT,
        )
    except subprocess.TimeoutExpired as exc:
        raise SmokeFailure(f"{binary} {' '.join(args)} timed out") from exc


def run(binary: str, timeout: float) -> None:
    version = run_command(binary, "--version")
    check(version.returncode == 0, f"--version exit={version.returncode}")
    check(version.stdout.strip(), "--version produced no version")
    check(not version.stderr, f"--version wrote stderr: {version.stderr!r}")
    print("PASS B-01 version")

    help_result = run_command(binary, "--help")
    check(help_result.returncode == 0, f"--help exit={help_result.returncode}")
    check(b"--mcp" in help_result.stdout and b"--mcp-http" in help_result.stdout, "--help does not mention MCP modes")
    check(not help_result.stderr, f"--help wrote stderr: {help_result.stderr!r}")
    print("PASS B-01 help")

    process = MCPProcess(binary, timeout)
    try:
        process.send(request(1, "server/discover"))
        discover_response = process.recv()
        check("error" not in discover_response, f"discovery error: {discover_response!r}")
        discover_result = discover_response.get("result", {})
        check(discover_result.get("resultType") == "complete", f"discovery resultType: {discover_result!r}")
        check(discover_result.get("supportedVersions") == [PROTOCOL_VERSION], f"supported versions: {discover_result!r}")
        check(isinstance(discover_result.get("capabilities", {}).get("tools"), dict), f"tools capability missing: {discover_result!r}")
        print("PASS B-02 discovery")

        process.send(request(2, "tools/list"))
        tools_response = process.recv()
        check("error" not in tools_response, f"tools/list error: {tools_response!r}")
        tools_result = tools_response.get("result", {})
        check(tools_result.get("resultType") == "complete", f"tools/list resultType: {tools_result!r}")
        tools = tools_result.get("tools")
        check(isinstance(tools, list), f"tools/list tools is not a list: {tools_result!r}")
        by_name = {tool.get("name"): tool for tool in tools if isinstance(tool, dict)}
        check(set(by_name) == {"web_fetch", "web_search"}, f"tool names: {sorted(by_name)!r}")
        for name, tool in by_name.items():
            check(tool.get("inputSchema", {}).get("type") == "object", f"{name} input schema: {tool!r}")
            check(isinstance(tool.get("outputSchema"), dict), f"{name} output schema missing: {tool!r}")
        fetch_input = by_name["web_fetch"].get("inputSchema", {})
        fetch_properties = fetch_input.get("properties", {})
        check(fetch_properties.get("render", {}).get("enum") == ["never", "auto", "always"], f"render schema: {fetch_input!r}")
        check(fetch_properties.get("render", {}).get("default") == "never", f"render default: {fetch_input!r}")
        check(fetch_properties.get("render_wait", {}).get("enum") == ["load", "networkidle"], f"render wait schema: {fetch_input!r}")
        fetch_output = by_name["web_fetch"].get("outputSchema", {})
        output_properties = fetch_output.get("properties", {})
        check(output_properties.get("rendered", {}).get("type") == "boolean", f"rendered output schema: {fetch_output!r}")
        check(output_properties.get("warnings", {}).get("type") == "array", f"warnings output schema: {fetch_output!r}")
        print("PASS B-03 tools/list")

        process.send(request(3, "tools/call", {"name": "web_search", "arguments": {}}))
        invalid_search = process.recv()
        invalid_result = invalid_search.get("result", {})
        check("error" not in invalid_search, f"invalid search became protocol error: {invalid_search!r}")
        check(invalid_result.get("resultType") == "complete", f"invalid search resultType: {invalid_result!r}")
        check(invalid_result.get("isError") is True, f"invalid search isError: {invalid_result!r}")
        check("query" in result_text(invalid_search).lower(), f"invalid search text: {invalid_search!r}")
        print("PASS B-04 invalid tool arguments")

        process.send({"jsonrpc": "2.0", "id": 4, "method": "tools/list", "params": {}})
        missing_meta = process.recv()
        check("error" in missing_meta, f"missing metadata was accepted: {missing_meta!r}")
        check(error_code(missing_meta) == -32602, f"missing metadata code: {missing_meta!r}")
        print("PASS B-05 missing metadata")

        process.send({
            "jsonrpc": "2.0",
            "id": 5,
            "method": "initialize",
            "params": {
                "protocolVersion": LEGACY_PROTOCOL_VERSION,
                "capabilities": {},
                "clientInfo": {"name": "jcode", "version": "test"},
            },
        })
        legacy = process.recv()
        check("error" not in legacy, f"legacy initialize failed: {legacy!r}")
        legacy_result = legacy.get("result", {})
        check(legacy_result.get("protocolVersion") == LEGACY_PROTOCOL_VERSION, f"legacy protocol version: {legacy!r}")
        check(legacy_result.get("serverInfo", {}).get("name") == "webfetch", f"legacy server info: {legacy!r}")
        print("PASS B-06 legacy initialize compatibility")

        process.send({"jsonrpc": "2.0", "id": 0, "method": "notifications/initialized"})
        process.send({"jsonrpc": "2.0", "id": 6, "method": "tools/list", "params": {}})
        legacy_tools = process.recv()
        check("error" not in legacy_tools, f"legacy tools/list failed: {legacy_tools!r}")
        legacy_tools_result = legacy_tools.get("result", {})
        legacy_tool_names = {tool.get("name") for tool in legacy_tools_result.get("tools", []) if isinstance(tool, dict)}
        check(legacy_tool_names == {"web_fetch", "web_search"}, f"legacy tools/list: {legacy_tools!r}")
        print("PASS B-07 legacy tools/list compatibility")

        process.send(request(7, "unknown/method"))
        unknown = process.recv()
        check("error" in unknown, f"unknown method was accepted: {unknown!r}")
        check(error_code(unknown) == -32601, f"unknown method code: {unknown!r}")
        print("PASS B-08 unknown method")

        process.send(request(8, "tools/call", {
            "name": "web_fetch",
            "arguments": {"url": "http://127.0.0.1:1/blocked"},
        }))
        ssrf = process.recv()
        ssrf_result = ssrf.get("result", {})
        check("error" not in ssrf, f"SSRF validation became protocol error: {ssrf!r}")
        check(ssrf_result.get("isError") is True, f"SSRF isError: {ssrf!r}")
        ssrf_text = result_text(ssrf).lower()
        check("private" in ssrf_text or "loopback" in ssrf_text, f"SSRF text: {ssrf!r}")
        print("PASS B-09 SSRF policy")

        process.send_raw('{"jsonrpc":"2.0","id":9,"method":"tools/list"')
        malformed = process.recv()
        check("error" in malformed, f"malformed JSON was accepted: {malformed!r}")
        check(error_code(malformed) == -32700, f"malformed JSON code: {malformed!r}")
        process.send(request(10, "server/discover"))
        after_malformed = process.recv()
        check("error" not in after_malformed, f"server did not recover after malformed JSON: {after_malformed!r}")
        print("PASS B-10 malformed input recovery")

        for line in process.stdout_lines:
            value = json.loads(line)
            check(isinstance(value, dict) and value.get("jsonrpc") == "2.0", f"bad stdout line: {line!r}")
        print("PASS B-11 JSON-RPC line discipline")
    except Exception:
        process.abort()
        raise

    return_code, stderr, _ = process.close()
    check(return_code == 0, f"MCP process exit={return_code}; stderr={stderr!r}")
    check(not stderr, f"MCP process wrote unexpected stderr: {stderr!r}")
    print("PASS B-12 clean EOF")
    print("PASS B-13 stderr discipline")

    bare = MCPProcess(binary, timeout, bare=True)
    try:
        bare.send({
            "jsonrpc": "2.0",
            "id": 14,
            "method": "initialize",
            "params": {
                "protocolVersion": LEGACY_PROTOCOL_VERSION,
                "capabilities": {},
                "clientInfo": {"name": "executable-only-host", "version": "test"},
            },
        })
        initialized = bare.recv()
        check(initialized.get("result", {}).get("protocolVersion") == LEGACY_PROTOCOL_VERSION, f"bare-path initialize: {initialized!r}")
    except Exception:
        bare.abort()
        raise
    return_code, stderr, _ = bare.close()
    check(return_code == 0, f"bare-path MCP exit={return_code}; stderr={stderr!r}")
    check(not stderr, f"bare-path MCP wrote unexpected stderr: {stderr!r}")
    print("PASS B-14 executable-only host startup")

    interrupted = MCPProcess(binary, timeout)
    try:
        interrupted.send(request(15, "server/discover"))
        check("error" not in interrupted.recv(), "interrupt probe did not become ready")
        interrupted.interrupt()
    except Exception:
        interrupted.abort()
        raise
    print("PASS B-15 SIGINT shutdown with open stdin")


def run_compact(binary: str, timeout: float) -> None:
    version = run_command(binary, "--version")
    check(version.returncode == 0, f"--version exit={version.returncode}")
    check(version.stdout.strip(), "--version produced no version")
    check(not version.stderr, f"--version wrote stderr: {version.stderr!r}")
    print("PASS C-01 version")

    help_result = run_command(binary, "--help")
    check(help_result.returncode == 0, f"--help exit={help_result.returncode}")
    check(b"--mcp --compact" in help_result.stdout, "--help does not mention compact MCP mode")
    check(not help_result.stderr, f"--help wrote stderr: {help_result.stderr!r}")
    print("PASS C-01 help")

    process = MCPProcess(binary, timeout, compact=True)
    try:
        process.send(request(101, "server/discover"))
        discovery = process.recv()
        check("error" not in discovery, f"compact discovery error: {discovery!r}")
        discovery_result = discovery.get("result", {})
        check(discovery_result.get("resultType") == "complete", f"compact discovery resultType: {discovery_result!r}")
        check(discovery_result.get("supportedVersions") == [PROTOCOL_VERSION], f"compact supported versions: {discovery_result!r}")
        print("PASS C-02 discovery")

        process.send(request(102, "tools/list"))
        tools_response = process.recv()
        check("error" not in tools_response, f"compact tools/list error: {tools_response!r}")
        tools_result = tools_response.get("result", {})
        tools = tools_result.get("tools")
        check(isinstance(tools, list) and len(tools) == 1, f"compact tools: {tools_result!r}")
        tool = tools[0]
        check(tool.get("name") == "webfetch", f"compact tool name: {tool!r}")
        schema = tool.get("inputSchema", {})
        check(schema.get("$schema") == "https://json-schema.org/draft/2020-12/schema", f"compact schema dialect: {schema!r}")
        check(isinstance(schema.get("oneOf"), list) and len(schema["oneOf"]) == 2, f"compact schema oneOf: {schema!r}")
        defs = schema.get("$defs", {})
        check(isinstance(defs, dict) and {"fetch_input", "search_input"} <= set(defs), f"compact schema defs: {schema!r}")
        compact_fetch_properties = defs["fetch_input"].get("properties", {})
        check(compact_fetch_properties.get("render", {}).get("enum") == ["never", "auto", "always"], f"compact render schema: {schema!r}")
        check(compact_fetch_properties.get("render_wait", {}).get("enum") == ["load", "networkidle"], f"compact render wait schema: {schema!r}")
        check(isinstance(tool.get("outputSchema"), dict), f"compact output schema missing: {tool!r}")
        print("PASS C-03 compact schema and one-tool exposure")

        process.send(request(103, "tools/call", {
            "name": "webfetch",
            "arguments": {"operation": "unknown", "input": {}},
        }))
        invalid = process.recv()
        invalid_result = invalid.get("result", {})
        check("error" not in invalid, f"compact invalid operation became protocol error: {invalid!r}")
        check(invalid_result.get("isError") is True, f"compact invalid operation: {invalid!r}")
        check("operation" in result_text(invalid).lower(), f"compact invalid operation text: {invalid!r}")
        print("PASS C-04 invalid operation")

        process.send(request(104, "tools/call", {
            "name": "webfetch",
            "arguments": {
                "operation": "fetch",
                "input": {"url": "http://127.0.0.1:1/blocked"},
            },
        }))
        ssrf = process.recv()
        ssrf_result = ssrf.get("result", {})
        check("error" not in ssrf, f"compact SSRF became protocol error: {ssrf!r}")
        check(ssrf_result.get("isError") is True, f"compact SSRF isError: {ssrf!r}")
        ssrf_text = result_text(ssrf).lower()
        check("private" in ssrf_text or "loopback" in ssrf_text, f"compact SSRF text: {ssrf!r}")
        print("PASS C-05 SSRF policy")

        process.send({"jsonrpc": "2.0", "id": 105, "method": "tools/list", "params": {}})
        missing_meta = process.recv()
        check("error" in missing_meta and error_code(missing_meta) == -32602, f"compact missing metadata: {missing_meta!r}")
        print("PASS C-06 missing metadata")

        process.send({
            "jsonrpc": "2.0",
            "id": 106,
            "method": "initialize",
            "params": {
                "protocolVersion": LEGACY_PROTOCOL_VERSION,
                "capabilities": {},
                "clientInfo": {"name": "jcode", "version": "test"},
            },
        })
        legacy = process.recv()
        check("error" not in legacy, f"compact legacy initialize: {legacy!r}")
        check(legacy.get("result", {}).get("protocolVersion") == LEGACY_PROTOCOL_VERSION, f"compact legacy version: {legacy!r}")
        print("PASS C-07 legacy initialize compatibility")

        process.send({"jsonrpc": "2.0", "id": 0, "method": "notifications/initialized"})
        process.send({"jsonrpc": "2.0", "id": 107, "method": "tools/list", "params": {}})
        legacy_tools = process.recv()
        check("error" not in legacy_tools, f"compact legacy tools/list: {legacy_tools!r}")
        legacy_tools_result = legacy_tools.get("result", {})
        legacy_tools_list = legacy_tools_result.get("tools", [])
        check(len(legacy_tools_list) == 1 and legacy_tools_list[0].get("name") == "webfetch", f"compact legacy tools: {legacy_tools!r}")
        print("PASS C-08 legacy tools/list compatibility")

        process.send(request(108, "unknown/method"))
        unknown = process.recv()
        check("error" in unknown and error_code(unknown) == -32601, f"compact unknown method: {unknown!r}")
        print("PASS C-09 unknown method")

        process.send_raw('{"jsonrpc":"2.0","id":109,"method":"tools/list"')
        malformed = process.recv()
        check("error" in malformed and error_code(malformed) == -32700, f"compact malformed JSON: {malformed!r}")
        process.send(request(110, "server/discover"))
        after_malformed = process.recv()
        check("error" not in after_malformed, f"compact recovery failed: {after_malformed!r}")
        print("PASS C-10 malformed input recovery")

        for line in process.stdout_lines:
            value = json.loads(line)
            check(isinstance(value, dict) and value.get("jsonrpc") == "2.0", f"bad compact stdout line: {line!r}")
        print("PASS C-11 JSON-RPC line discipline")
    except Exception:
        process.abort()
        raise

    return_code, stderr, _ = process.close()
    check(return_code == 0, f"compact MCP process exit={return_code}; stderr={stderr!r}")
    check(not stderr, f"compact MCP process wrote unexpected stderr: {stderr!r}")
    print("PASS C-12 clean EOF")
    print("PASS C-13 stderr discipline")


def free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


class MCPHTTPProcess:
    def __init__(self, binary: str, listen: str, env: dict[str, str] | None = None) -> None:
        process_env = os.environ.copy()
        process_env["WEBFETCH_MCP_LOG_LEVEL"] = "off"
        if env:
            process_env.update(env)
        self.proc = subprocess.Popen(
            [binary, "--mcp-http", "--listen", listen],
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            env=process_env,
        )
        self.base_url = f"http://127.0.0.1:{listen.rsplit(':', 1)[1]}"

    def wait_ready(self, timeout: float, token: str = "") -> None:
        deadline = time.monotonic() + timeout
        payload = {"jsonrpc": "2.0", "id": 1, "method": "server/discover", "params": {"_meta": metadata()}}
        headers = {"Mcp-Protocol-Version": PROTOCOL_VERSION, "Mcp-Method": "server/discover"}
        if token:
            headers["Authorization"] = f"Bearer {token}"
        while time.monotonic() < deadline:
            if self.proc.poll() is not None:
                stderr = self.proc.stderr.read().decode("utf-8", errors="replace") if self.proc.stderr else ""
                raise SmokeFailure(f"HTTP MCP process exited early: {stderr!r}")
            try:
                http_json(self.base_url, payload, headers)
                return
            except (HTTPError, URLError, ConnectionError, TimeoutError):
                time.sleep(0.03)
        raise SmokeFailure("timed out waiting for HTTP MCP listener")

    def close(self, timeout: float) -> tuple[int, str]:
        if self.proc.poll() is None:
            self.proc.terminate()
        try:
            return_code = self.proc.wait(timeout=timeout)
        except subprocess.TimeoutExpired as exc:
            self.proc.kill()
            self.proc.wait()
            raise SmokeFailure("HTTP MCP process did not terminate") from exc
        stderr = self.proc.stderr.read().decode("utf-8", errors="replace") if self.proc.stderr else ""
        return return_code, stderr


def http_json(base_url: str, payload: dict[str, Any], headers: dict[str, str] | None = None) -> dict[str, Any]:
    request_headers = {"Content-Type": "application/json", "Accept": "application/json"}
    if headers:
        request_headers.update(headers)
    request = Request(
        base_url,
        data=json.dumps(payload, separators=(",", ":")).encode("utf-8"),
        headers=request_headers,
        method="POST",
    )
    with urlopen(request, timeout=DEFAULT_TIMEOUT) as response:
        return json.loads(response.read().decode("utf-8"))


def http_status(base_url: str, payload: dict[str, Any], headers: dict[str, str] | None = None) -> int:
    request_headers = {"Content-Type": "application/json", "Accept": "application/json"}
    if headers:
        request_headers.update(headers)
    request = Request(
        base_url,
        data=json.dumps(payload, separators=(",", ":")).encode("utf-8"),
        headers=request_headers,
        method="POST",
    )
    try:
        with urlopen(request, timeout=DEFAULT_TIMEOUT) as response:
            return response.status
    except HTTPError as exc:
        return exc.code


def run_http(binary: str, timeout: float) -> None:
    help_result = run_command(binary, "--help")
    check(help_result.returncode == 0, f"HTTP help exit={help_result.returncode}")
    check(b"--mcp-http" in help_result.stdout and b"mcp list" in help_result.stdout, "--help omits HTTP or explorer mode")
    print("PASS H-01 help")

    port = free_port()
    process = MCPHTTPProcess(binary, f"127.0.0.1:{port}")
    discovery_payload = {
        "jsonrpc": "2.0",
        "id": 1,
        "method": "server/discover",
        "params": {"_meta": metadata()},
    }
    discovery_headers = {"Mcp-Protocol-Version": PROTOCOL_VERSION, "Mcp-Method": "server/discover"}
    try:
        process.wait_ready(timeout)
        discovery = http_json(process.base_url, discovery_payload, discovery_headers)
        check(discovery.get("result", {}).get("supportedVersions") == [PROTOCOL_VERSION], f"HTTP discovery: {discovery!r}")
        print("PASS H-02 HTTP discovery")

        missing_header = http_status(process.base_url, discovery_payload, {"Mcp-Method": "server/discover"})
        check(missing_header == 400, f"HTTP missing header status={missing_header}")
        print("PASS H-03 HTTP header validation")

        tools = http_json(process.base_url, {"jsonrpc": "2.0", "id": 2, "method": "tools/list", "params": {"_meta": metadata()}}, {"Mcp-Protocol-Version": PROTOCOL_VERSION, "Mcp-Method": "tools/list"})
        tools_result = tools.get("result", {})
        check(tools_result.get("ttlMs") == 60000 and tools_result.get("cacheScope") == "public", f"HTTP cache controls: {tools!r}")
        print("PASS H-04 HTTP cache controls")
    finally:
        return_code, stderr = process.close(timeout)
        check(return_code == 0, f"HTTP MCP exit={return_code}; stderr={stderr!r}")
        check(not stderr, f"HTTP MCP wrote unexpected stderr: {stderr!r}")

    protected_port = free_port()
    protected = MCPHTTPProcess(binary, f"0.0.0.0:{protected_port}", {"WEBFETCH_MCP_BEARER_TOKEN": "smoke-token"})
    try:
        protected.wait_ready(timeout, "smoke-token")
        missing_auth = http_status(protected.base_url, discovery_payload, discovery_headers)
        check(missing_auth == 401, f"HTTP missing auth status={missing_auth}")
        valid_auth = http_json(protected.base_url, discovery_payload, {**discovery_headers, "Authorization": "Bearer smoke-token"})
        check("error" not in valid_auth, f"HTTP bearer auth response: {valid_auth!r}")
        print("PASS H-05 HTTP bearer auth")
    finally:
        return_code, stderr = protected.close(timeout)
        check(return_code == 0, f"protected HTTP MCP exit={return_code}; stderr={stderr!r}")
        check(not stderr, f"protected HTTP MCP wrote unexpected stderr: {stderr!r}")
    print("PASS H-06 HTTP shutdown")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", default="webfetch", help="path to the installed webfetch binary")
    parser.add_argument("--timeout", type=float, default=DEFAULT_TIMEOUT, help="per-response timeout in seconds")
    parser.add_argument("--mode", choices=("full", "compact", "http"), default="full", help="MCP surface to smoke test")
    args = parser.parse_args()
    try:
        if args.mode == "compact":
            run_compact(args.binary, args.timeout)
        elif args.mode == "http":
            run_http(args.binary, args.timeout)
        else:
            run(args.binary, args.timeout)
    except (OSError, SmokeFailure) as exc:
        print(f"FAIL: {exc}", file=sys.stderr)
        return 1
    print(f"mcp_{args.mode}_smoke=passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
