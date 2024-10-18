import os
from openai import OpenAI, AzureOpenAI

client = OpenAI(
    # This is the default and can be omitted
    base_url="http://localhost:8080/openai/v1",
    api_key="test",
)

chat_completion = client.chat.completions.create(
    messages=[
        {
            "role": "user",
            "content": "Say this is a test",
        }
    ],
    model="gpt-3.5-turbo",
    stream=False,
)

print(chat_completion.choices[0].message.content)


# chat_completion = client.chat.completions.create(
#     messages=[
#         {
#             "role": "user",
#             "content": "Write me a long story",
#         }
#     ],
#     model="gpt-3.5-turbo",
#     stream=True,
# )

# for i in chat_completion:
#     print(i.choices[0].delta.content, end="")

# print()


client = AzureOpenAI(
    # This is the default and can be omitted
    base_url="http://localhost:8080/azure",
    api_key="test",
    api_version="test"
)

completion = client.chat.completions.create(
  model="gpt-3.5-turbo",
  messages=[
    {"role": "system", "content": "You are a helpful assistant."},
    {"role": "user", "content": "Hello!"}
  ]
)

print(completion)