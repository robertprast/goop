# Goop - GO Openllm Proxy

Goop is a go based reverse proxy meant to be a single interface for multi-cloud LLM deployments and SaaS API deployments. Supported engines as of now are `OpenAI`, `AzureOpenAI`, `Vertex AI (Google)` and `Bedrock`. 

Additionally, there is a common `OpenAI proxy` to allow for a single interface based on OpenAI schemas for all possible models for bedrock and vertex . This allows you to pass `bedrock/<model_id>` to the OpenAI sdk as the `model`. 

- [Architecture](#architecture)
- [Setup and Installation](#setup-and-installation)
- [Usage](#usage)
- [Advanced Usage](#advanced-usage)

## Architecture

This reverse proxy integrates multiple LLM providers (e.g., OpenAI, Bedrock, Azure) using a modular and efficient approach:

1. **Engine Network Interface**:
   - Each LLM provider is proxied at the network level, allowing upstream clients to use their native SDKs seamlessly. This allows infra changes to happen indepdendant of the application layer to passthrough. For example, you can enable a new bedrock model in the AWS console and have instant support from the persecptive of the reverse proxy. 

2. **Dynamic Engine Routing**:
   - Middleware dynamically routes requests based on URL prefixes to the appropriate engine:
     - `/openai` for the OpenAI LLM engine.
     - `/bedrock` for the Bedrock (Anthropic) engine.
     - `/azure` for the Azure OpenAI engine.
     - `/vertex` for Google Vertex AI engine
     - `/openai-proxy` for OpenAI interfaces for Bedrock/Vertex based LLMs

3. **Pre and Post-Response Hooks**:
   - Engines integrate with the audit package to log inline hooks on raw request/response structs. The proxy supports non-blocking SSE/streaming, and the post-response hook is triggered only after the client connection is closed.


## Setup and Installation

1. **Clone the repository**:
   ```bash
   git clone https://github.com/robertpast/goop
   cd goop
   ```

2. Build the Go application:
   ```bash
   make build
   ```

3. Run the server:
   ```bash
   make run
   ```

4. (Optional) Build and run the Docker container:
   ```bash
   make build-docker
   make run-docker
   ```

## Usage 

#### OpenAI Client

```python
from openai import OpenAI, AzureOpenAI
client = OpenAI(
    base_url="http://localhost:8080/openai/v1",
    api_key="test",
)
```

#### Azure Client
```python
azureClient = AzureOpenAI(
    base_url="http://localhost:8080/azure",
    api_key="test",
    api_version="test",
)
```

### Bedrock Client
```python
def _replace_headers(request: AWSRequest, **kwargs):
    request.headers = {"Authorization": "Bearer test"}

client = boto3.client("bedrock-runtime", endpoint_url="http://localhost:8080/bedrock")
client.meta.events.register("before-send.bedrock-runtime.*", _replace_headers)
```

### Vertex AI (Google)
```python
import vertexai
from vertexai.preview.generative_models import GenerativeModel

PROJECT_ID = "<YOUR_VERTEX_AI_PROJECT_ID>"
vertexai.init(
    project=PROJECT_ID,
    api_endpoint="http://localhost:8080/vertex",
)

generative_multimodal_model = GenerativeModel("gemini-1.5-flash-002")
response = generative_multimodal_model.generate_content(["Say hi"])

print(response)
```

## Advanced Usage

#### Using the OpenAI SDK for bedrock based models

```python
from openai import OpenAI, AzureOpenAI

client = OpenAI(
    base_url="http://localhost:8080/openai-proxy/v1",
    api_key="test",
)
chat_completion = client.chat.completions.create(
    messages=[
        {
            "role": "user",
            "content": "Whats up dog?",
        }
    ],
    model="bedrock/us.anthropic.claude-3-haiku-20240307-v1:0",
    stream=False,
)

print(chat_completion)
print(chat_completion.choices[0].message.content)
```



#### ELL Framework with all clients
Chaining multiple native LLM SDK clients that flow through a single agentic framework and proxy all requests to a single reverse proxy service

For more information on the ELL framework, visit the [ELL GitHub repository](https://github.com/MadcowD/ell/).

```python
import ell
from pydantic import Field
import requests
from bs4 import BeautifulSoup

ell.init(verbose=True)

"""
TOOL USAGE
"""


@ell.tool()
def get_html_content(
    url: str = Field(
        description="The URL to get the HTML content of. Never include the protocol (like http:// or https://)"
    ),
):
    """Get the HTML content of a URL."""
    response = requests.get("https://" + url)
    soup = BeautifulSoup(response.text, "html.parser")
    return soup.get_text()[:100]


# OpenAI Client 
@ell.complex(
    model="gpt-4o-mini",
    tools=[get_html_content],
    client=client,
)
def openai_get_website_content(website: str) -> str:
    return f"Tell me what's on {website}"


print("OpenAI Client Tool Use\n\n")
output = openai_get_website_content("new york times front page")
if output.tool_calls:
    print(output.tool_calls[0]())


# Bedrock Client
@ell.complex(
    model="anthropic.claude-3-haiku-20240307-v1:0",
    tools=[get_html_content],
    client=bedrockClient,
)
def bedrock_get_website_content(website: str) -> str:
    """You are an agent that can summarize the contents of a website."""
    return f"Tell me what's on {website}"


print("\n\nBedrock Client Tool Use\n\n")
output = bedrock_get_website_content("new york times front page")
if output.tool_calls:
    print(output.tool_calls[0]())


```