# Streamed
curl http://localhost:5000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -N \
  -d '{
        "model": "Qwen/Qwen2.5-7B-Instruct",
        "temperature": 0.8,
        "stream": true,
        "stream_options": { "include_usage": true },
        "messages": [
          { "role": "user", "content": "Hi!" }
        ]
      }'

# Non-streamed
curl http://localhost:5000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -N \
  -d '{
        "model": "Qwen/Qwen2.5-7B-Instruct",
        "temperature": 0.8,
        "messages": [
          { "role": "user", "content": "Hi!" }
        ]
      }'
