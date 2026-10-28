#!/usr/bin/env python3
# Copyright 2026 Metrum AI
# SPDX-License-Identifier: Apache-2.0

"""Real SDK agent loops used by the deterministic localhost Chat smoke."""
import asyncio
import os
import sys
from pathlib import Path

BASE = os.environ["ROUTER_BASE_URL"]
TOKEN = os.environ["ROUTER_CALLER_TOKEN"]


def write_fixture(path: str, content: str) -> str:
    if (path, content) not in {("result.txt", "tool-result-42\n"), ("result2.txt", "tool-result-43\n")}:
        raise ValueError("mock requested an unexpected write")
    Path(path).write_text(content, encoding="utf-8")
    return "Successfully wrote the requested fixture."


def langgraph():
    from langchain_core.tools import tool
    from langchain_openai import ChatOpenAI
    from langgraph.prebuilt import create_react_agent

    @tool
    def write(path: str, content: str) -> str:
        """Write the requested test fixture."""
        return write_fixture(path, content)

    model = ChatOpenAI(model="chat", base_url=BASE, api_key=TOKEN, streaming=True, max_retries=0)
    graph = create_react_agent(model, [write])
    result = graph.invoke({"messages": [("user", "Write the requested file and finish.")]}, {"recursion_limit": 6})
    assert result["messages"][-1].content == "Completed."


async def openai_agents():
    from agents import Agent, OpenAIChatCompletionsModel, RunConfig, Runner, function_tool
    from openai import AsyncOpenAI
    from pydantic import BaseModel

    class Outcome(BaseModel):
        status: str
        count: int

    @function_tool
    def write(path: str, content: str) -> str:
        """Write the requested test fixture."""
        return write_fixture(path, content)

    client = AsyncOpenAI(base_url=BASE, api_key=TOKEN, max_retries=0)
    try:
        model = OpenAIChatCompletionsModel(model="chat", openai_client=client)
        strict_output = os.environ.get("AGENT_COMPAT_SCENARIO") == "strict-json"
        agent = Agent(name="fixture-agent", model=model, tools=[write], output_type=Outcome if strict_output else str)
        result = Runner.run_streamed(agent, "Write the requested file and finish.", max_turns=3, run_config=RunConfig(tracing_disabled=True))
        async for _ in result.stream_events():
            pass
        if strict_output:
            assert isinstance(result.final_output, Outcome)
            assert result.final_output.status == "completed" and result.final_output.count == 42
        else:
            assert result.final_output == "Completed."
    finally:
        await client.close()


if sys.argv[1] == "langgraph":
    langgraph()
elif sys.argv[1] == "openai-agents":
    asyncio.run(openai_agents())
else:
    raise SystemExit("unsupported client")
print("agent tool loop complete")
