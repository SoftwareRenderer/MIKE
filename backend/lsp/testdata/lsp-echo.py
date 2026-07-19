#!/usr/bin/env python3
"""Minimal LSP echo server for testing.

Reads Content-Length framed JSON-RPC messages from stdin and echoes them
back with proper Content-Length framing on stdout.

Usage:
    python3 lsp-echo.py                          # plain echo
    python3 lsp-echo.py --stderr "some message"  # write to stderr first, then echo
    python3 lsp-echo.py --initial-notif '{"jsonrpc":"2.0",...}'  # emit initial notification, then echo
"""
import sys
import argparse


def read_message_bin(stream):
    """Read one Content-Length framed message from a binary stream."""
    header_line = b""
    while True:
        chunk = stream.read(1)
        if not chunk:
            return None
        header_line += chunk
        if header_line.endswith(b"\r\n"):
            break
    length = int(header_line.split(b":", 1)[1].strip())
    # Read blank separator line
    sep = stream.read(2)  # \r\n
    return stream.read(length)


def write_message_bin(stream, body: bytes):
    """Write a Content-Length framed message to a binary stream."""
    stream.write(f"Content-Length: {len(body)}\r\n\r\n".encode())
    stream.write(body)
    stream.flush()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--stderr", default=None, help="Message to write to stderr before starting")
    parser.add_argument("--initial-notif", default=None, help="Initial notification to emit before echo loop")
    args = parser.parse_args()

    if args.stderr:
        sys.stderr.write(args.stderr + "\n")
        sys.stderr.flush()

    if args.initial_notif:
        body = args.initial_notif.encode()
        write_message_bin(sys.stdout.buffer, body)

    while True:
        body = read_message_bin(sys.stdin.buffer)
        if body is None:
            break
        write_message_bin(sys.stdout.buffer, body)


if __name__ == "__main__":
    main()
