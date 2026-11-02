#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0
"""Exercise get-uri's FTP download against a local, synthetic FTP server."""
from pathlib import Path
import os
import socket
import socketserver
import subprocess
import threading
import unittest


PAYLOAD = b"synthetic-ftp-fixture\n"


class FTPHandler(socketserver.StreamRequestHandler):
    def handle(self):
        self.request.settimeout(15)
        data_socket = None
        self.wfile.write(b"220 synthetic FTP ready\r\n")
        try:
            while line := self.rfile.readline():
                command = line.decode("ascii", "replace").strip().split(" ", 1)[0].upper()
                if command == "USER":
                    reply = b"331 password required\r\n"
                elif command == "PASS":
                    reply = b"230 logged in\r\n"
                elif command == "FEAT":
                    reply = b"211-Features\r\n MDTM\r\n EPSV\r\n211 End\r\n"
                elif command == "MDTM":
                    reply = b"213 20260101000000\r\n"
                elif command == "PWD":
                    reply = b'257 "/" is current directory\r\n'
                elif command in ("TYPE", "OPTS", "STRU", "MODE", "CWD"):
                    reply = b"200 OK\r\n"
                elif command == "EPSV":
                    data_socket = socket.socket()
                    data_socket.settimeout(15)
                    data_socket.bind(("127.0.0.1", 0))
                    data_socket.listen(1)
                    reply = f"229 Entering Extended Passive Mode (|||{data_socket.getsockname()[1]}|)\r\n".encode()
                elif command == "RETR" and data_socket is not None:
                    self.wfile.write(b"150 sending file\r\n")
                    with data_socket.accept()[0] as conn:
                        conn.sendall(PAYLOAD)
                    data_socket.close()
                    data_socket = None
                    reply = b"226 transfer complete\r\n"
                elif command == "QUIT":
                    self.wfile.write(b"221 goodbye\r\n")
                    return
                else:
                    reply = b"502 unsupported command\r\n"
                self.wfile.write(reply)
        finally:
            if data_socket is not None:
                data_socket.close()


class FTPOverrideTest(unittest.TestCase):
    def test_get_uri_basic_ftp_download(self):
        modules = Path(os.environ["EVAL_NODE_MODULES"]).resolve()
        script = """
          const { pathToFileURL } = require('node:url');
          (async () => {
            const { getUri } = await import(pathToFileURL(process.argv[1]).href);
            const stream = await getUri(process.argv[2]);
            let data = ''; for await (const chunk of stream) data += chunk;
            if (data !== 'synthetic-ftp-fixture\\n') throw new Error('FTP output mismatch');
          })().catch(err => { console.error(err); process.exit(1); });
        """
        with socketserver.ThreadingTCPServer(("127.0.0.1", 0), FTPHandler) as server:
            thread = threading.Thread(target=server.serve_forever, daemon=True)
            thread.start()
            try:
                subprocess.run(["node", "-e", script, str(modules / "get-uri/dist/index.js"), f"ftp://127.0.0.1:{server.server_address[1]}/fixture"], check=True, timeout=30)
            finally:
                server.shutdown()
                thread.join(timeout=5)


if __name__ == "__main__":
    unittest.main()
