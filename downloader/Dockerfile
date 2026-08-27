FROM denoland/deno:bin-2.9.5 AS deno

FROM python:3.11-slim

WORKDIR /app

# yt-dlp enables Deno automatically for YouTube's JavaScript challenges.
COPY --from=deno /deno /usr/local/bin/deno

# Install ffmpeg and build dependencies for curl-cffi.
RUN apt-get update && \
    apt-get install -y ffmpeg gcc libcurl4-openssl-dev libssl-dev && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*

# Install dependencies
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

# Copy application code
COPY . .

# Run the worker
CMD ["python", "worker.py"]
