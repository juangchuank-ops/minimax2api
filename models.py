"""Pydantic models for OpenAI-compatible API."""

from typing import List, Optional, Union, Dict, Any
from pydantic import BaseModel, Field, ConfigDict, model_validator


class Message(BaseModel):
    model_config = ConfigDict(extra="allow")
    role: str
    content: Union[str, List[Dict[str, Any]], None] = None
    name: Optional[str] = None
    tool_calls: Optional[List[Dict[str, Any]]] = None
    tool_call_id: Optional[str] = None


class ToolFunction(BaseModel):
    model_config = ConfigDict(extra="allow")
    name: str
    description: Optional[str] = None
    parameters: Optional[Dict[str, Any]] = None


class Tool(BaseModel):
    model_config = ConfigDict(extra="allow")
    type: str = "function"
    function: ToolFunction


class ChatCompletionRequest(BaseModel):
    model_config = ConfigDict(extra="allow")
    model: str = "MiniMax-M2.7"
    messages: List[Message]
    temperature: Optional[float] = 1.0
    top_p: Optional[float] = 0.95
    max_tokens: Optional[int] = None
    stream: Optional[bool] = False
    tools: Optional[List[Tool]] = None
    tool_choice: Optional[Union[str, Dict[str, Any]]] = None
    stop: Optional[Union[str, List[str]]] = None
    presence_penalty: Optional[float] = None
    frequency_penalty: Optional[float] = None
    reasoning_split: Optional[bool] = False
    extra_body: Optional[Dict[str, Any]] = Field(default_factory=dict)

    @model_validator(mode="before")
    @classmethod
    def capture_extra_fields(cls, data: Any) -> Any:
        if isinstance(data, dict):
            known = {
                "model", "messages", "temperature", "top_p", "max_tokens",
                "stream", "tools", "tool_choice", "stop", "presence_penalty",
                "frequency_penalty", "reasoning_split",
            }
            extra = {k: v for k, v in data.items() if k not in known}
            if extra:
                existing = data.get("extra_body") or {}
                data = {**data, "extra_body": {**existing, **extra}}
        return data


class ChoiceMessage(BaseModel):
    role: str = "assistant"
    content: Optional[str] = None
    tool_calls: Optional[List[Dict[str, Any]]] = None


class Choice(BaseModel):
    index: int = 0
    message: Optional[ChoiceMessage] = None
    delta: Optional[Dict[str, Any]] = None
    finish_reason: Optional[str] = None


class Usage(BaseModel):
    prompt_tokens: int = 0
    completion_tokens: int = 0
    total_tokens: int = 0


class ChatCompletionResponse(BaseModel):
    id: str
    object: str = "chat.completion"
    created: int
    model: str
    choices: List[Choice]
    usage: Optional[Usage] = None
