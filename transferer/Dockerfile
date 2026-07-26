FROM python:3.12-slim
WORKDIR /app
COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt
COPY . .
CMD ["gunicorn", "-b", "0.0.0.0:8080", "-w", "1", "--worker-class", "gthread", "--threads", "16", "--timeout", "600", "app:app"]
