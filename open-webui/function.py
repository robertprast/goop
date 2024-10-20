from typing import Union, Iterator
import boto3
import vertexai
from vertexai.preview.generative_models import GenerativeModel
from openai import OpenAI, AzureOpenAI


class Pipe:
    def __init__(self):
        self.type = "manifold"
        self.openai_client = OpenAI(
            base_url="http://host.docker.internal:8080/openai/v1",
            api_key="test",
        )
        self.azure_client = AzureOpenAI(
            base_url="http://host.docker.internal:8080/azure",
            api_key="test",
            api_version="test",
        )
        self.bedrock_client = boto3.client(
            "bedrock-runtime",
            endpoint_url="http://host.docker.internal:8080/bedrock",
            region_name="us-east-1",
            aws_access_key_id="test",
            aws_secret_access_key="test",
        )

        from google.auth.credentials import AnonymousCredentials

        class DummyCredentials(AnonymousCredentials):
            pass

        self.vertex_endpoint = "http://host.docker.internal:8080/vertex"
        self.vertex_project_id = "encrypted-llm"
        vertexai.init(
            project=self.vertex_project_id,
            api_endpoint=self.vertex_endpoint,
            credentials=DummyCredentials(),
            api_transport="rest",
        )

    def pipes(self):
        return [
            ##################
            # OPENAI MODELS  #
            ##################
            {
                "id": "openai/gpt-4o",
                "name": "openai/gpt-4o",
            },
            {
                "id": "openai/gpt-4o-mini",
                "name": "openai/gpt-4o-mini",
            },
            # {
            #     "id": "azure/gpt-4o",
            #     "name": "azure/gpt-4o",
            # },
            ##################
            # BEDROCK MODELS #
            ##################
            {
                "id": "bedrock/us.anthropic.claude-3-haiku-20240307-v1:0",
                "name": "bedrock/claude-3-haiku",
            },
            {
                "id": "bedrock/us.anthropic.claude-3-5-sonnet-20240620-v1:0",
                "name": "bedrock/claude-3-5-sonnet",
            },
            {
                "id": "bedrock/us.meta.llama3-2-11b-instruct-v1:0",
                "name": "bedrock/llama3.2-11b",
            },
            {
                "id": "bedrock/us.meta.llama3-2-1b-instruct-v1:0",
                "name": "bedrock/llama3.2-1b",
            },
            {
                "id": "bedrock/us.meta.llama3-2-3b-instruct-v1:0",
                "name": "bedrock/llama3.2-3b",
            },
            {
                "id": "bedrock/us.meta.llama3-2-90b-instruct-v1:0",
                "name": "bedrock/llama3.2-90b",
            },
            # {
            #     "id": "bedrock/us.anthropic.claude-3-opus-20240229-v1:0",
            #     "name": "bedrock/claude-3-opus",
            # },
            # {
            #     "id": "bedrock/us.anthropic.claude-3-sonnet-20240229-v1:0",
            #     "name": "bedrock/claude-3-sonnet",
            # },
            ##################
            # VERTEX MODELS  #
            ##################
            {
                "id": "vertex/gemini-1.5-flash-002",
                "name": "vertex/gemini-1.5-flash",
            },
            {
                "id": "vertex/gemini-1.5-pro-002",
                "name": "vertex/gemini-1.5-pro",
            },
        ]

    def pipe(self, body: dict, __user__: dict) -> Union[str, Iterator]:
        model_name = body["model"].replace("openai_proxy_pipe.", "")
        if model_name.startswith("openai/"):
            body["model"] = model_name.replace("openai/", "")
            return self._handle_openai(body)

        elif model_name.startswith("azure/"):
            body["model"] = model_name.replace("azure/", "")
            return self._handle_azure(body)
        elif model_name.startswith("bedrock/"):
            body["model"] = model_name.replace("bedrock/", "")
            return self._handle_bedrock(body)
        elif model_name.startswith("vertex/"):
            body["model"] = model_name.replace("vertex/", "")
            return self._handle_vertex(body)
        else:
            return f"Error: Unsupported model prefix for {model_name}"

    def _handle_openai(self, body: dict):
        model_id = body["model"]
        payload = {**body, "model": model_id}
        try:
            response = self.openai_client.chat.completions.create(**payload)
            if body.get("stream", False):

                def stream_generator():
                    for i in response:
                        if len(i.choices) > 0 and i.choices[0].delta.content:
                            yield i.choices[0].delta.content

                return stream_generator()
            else:
                return response.choices[0].message.content
        except Exception as e:
            return f"Error: {e}"

    def _handle_azure(self, body: dict):
        model_id = body["model"]
        payload = {**body, "model": model_id}
        try:
            response = self.azure_client.chat.completions.create(**payload)
            if body.get("stream", False):

                def stream_generator():
                    for i in response:
                        if len(i.choices) > 0 and i.choices[0].delta.content:
                            yield i.choices[0].delta.content

                return stream_generator()
            else:
                return response.choices[0].message.content
        except Exception as e:
            return f"Error: {e}"

    def _handle_bedrock(self, body: dict):
        model_id = body["model"]
        try:

            def _replace_headers(request, **kwargs):
                request.headers["Authorization"] = "Bearer test"

            self.bedrock_client.meta.events.register(
                "before-send.bedrock-runtime.*", _replace_headers
            )
            conversation = []
            for message in body.get("messages", []):
                conversation.append(
                    {
                        "role": message["role"],
                        "content": [{"text": message["content"]}],
                    }
                )
            if body.get("stream", False):

                def stream_generator():
                    streaming_response = self.bedrock_client.converse_stream(
                        modelId=model_id,
                        messages=conversation,
                    )

                    for chunk in streaming_response["stream"]:
                        if "contentBlockDelta" in chunk:
                            text = chunk["contentBlockDelta"]["delta"]["text"]
                            if text:
                                yield text

                return stream_generator()

            response = self.bedrock_client.converse(
                modelId=model_id,
                messages=conversation,
            )
            response_text = response["output"]["message"]["content"][0]["text"]
            if response_text:
                return response_text
            else:
                raise Exception("No response text found in the response.")
        except Exception as e:
            return f"Error: {e}"

    def _handle_vertex(self, body: dict):
        model_id = body["model"]
        try:
            generative_model = GenerativeModel(model_id)
            contents = [
                {"role": message["role"], "parts": [{"text": message["content"]}]}
                for message in body.get("messages", [])
            ]

            if body.get("stream", False):

                def stream_generator():
                    # TODO - Implement streaming for VertexAI, this is a blocking call for now
                    response = generative_model.generate_content(
                        contents,
                        generation_config=body.get("generation_config", {}),
                        safety_settings=body.get("safety_settings", {}),
                        stream=True,
                    )
                    for chunk in response:
                        if (
                            len(chunk.candidates) > 0
                            and chunk.candidates[0].content.parts[0].text
                        ):
                            yield chunk.candidates[0].content.parts[0].text

                return stream_generator()
            else:
                response = generative_model.generate_content(
                    contents,
                    generation_config=body.get("generation_config", {}),
                    safety_settings=body.get("safety_settings", {}),
                )
                return response.candidates[0].content.parts[0].text
        except Exception as e:
            return f"Error: {e}"
