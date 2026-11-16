#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Exercise the router MCP endpoint with the pinned Python MCP SDK."""
from __future__ import annotations

import asyncio
import sys

import httpx2 as httpx
from mcp import ClientSession
from mcp.client.streamable_http import streamable_http_client


async def main(url: str, token: str) -> None:
    async with httpx.AsyncClient(headers={"Authorization": f"Bearer {token}"}) as http_client:
        async with streamable_http_client(url, http_client=http_client) as (read_stream, write_stream):
            async with ClientSession(read_stream, write_stream) as session:
                await session.initialize()
                tools = await session.list_tools()
                names = {tool.name for tool in tools.tools}
                if "local/echo" not in names:
                    raise RuntimeError(f"MCP tool missing from list: {sorted(names)}")
                result = await session.call_tool("local/echo", {"text": "continue through MCP"})
                text = "\n".join(item.text for item in result.content if hasattr(item, "text"))
                if "mcp-result-42" not in text:
                    raise RuntimeError(f"MCP tool result missing: {text}")


if __name__ == "__main__":
    if len(sys.argv) != 3:
        raise SystemExit("usage: mcp_sdk_smoke.py <router-mcp-url> <caller-token>")
    asyncio.run(main(sys.argv[1], sys.argv[2]))
