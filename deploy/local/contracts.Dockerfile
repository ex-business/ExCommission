FROM python:3.12-slim@sha256:a6e34c598f2467ed0e9a8d349809fcd8b5c603269512df273a0bb1784edc11b1
WORKDIR /app
COPY tools/requirements.txt /app/tools/requirements.txt
RUN pip install --no-cache-dir -r tools/requirements.txt
COPY tools /app/tools
COPY contracts /app/contracts
COPY conformance /app/conformance
CMD ["python", "tools/test_contracts.py"]
