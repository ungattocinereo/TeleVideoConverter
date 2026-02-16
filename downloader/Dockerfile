FROM python:3.11-slim

WORKDIR /app

# Install ffmpeg and build dependencies for curl-cffi
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
