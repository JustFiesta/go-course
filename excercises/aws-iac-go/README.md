# AWS IaC w Go — DynamoDB + Lambda + CloudWatch

Projekt tworzy kompletną infrastrukturę AWS za pomocą Go + AWS SDK v2.

## Struktura projektu

```
aws-iac-go/
├── main.go                    # Orkiestrator — uruchamia deployment w kolejności
├── go.mod                     # Zależności Go
│
├── config/
│   └── config.go              # Centralna konfiguracja (env vars + defaults)
│
├── utils/
│   └── waiter.go              # RetryWithBackoff, PollUntil, ZipFiles
│
├── infrastructure/
│   ├── iam.go                 # Rola IAM + polityki dla Lambdy
│   ├── dynamodb.go            # Tabela DynamoDB (PAY_PER_REQUEST + PITR)
│   ├── sns.go                 # SNS Topic dla alertów
│   ├── lambda.go              # Deploy funkcji Lambda (pakuje handler.py → ZIP)
│   └── cloudwatch.go          # Log Group + 3 alarmy + Dashboard
│
└── lambda_src/
    └── handler.py             # Kod Lambdy w Pythonie (fetch → validate → store)
```

## Wymagania

- Go 1.21+
- AWS CLI skonfigurowane (`aws configure`) lub zmienne środowiskowe:
  ```
  AWS_ACCESS_KEY_ID=...
  AWS_SECRET_ACCESS_KEY=...
  AWS_REGION=us-east-1
  ```
- Uprawnienia IAM: `iam:*`, `dynamodb:*`, `lambda:*`, `cloudwatch:*`, `logs:*`, `sns:*`

## Instalacja

```bash
# 1. Klonuj/skopiuj projekt
cd aws-iac-go

# 2. Pobierz zależności
go mod tidy

# 3. Zbuduj
go build -o iac-deploy .
```

## Użycie

### Deploy infrastruktury

```bash
go run .
```

### Deploy + test wywołania Lambdy

```bash
go run . -test
```

### Usuń całą infrastrukturę

```bash
go run . -destroy
```

## Konfiguracja przez zmienne środowiskowe

| Zmienna               | Domyślna wartość                               | Opis                           |
|-----------------------|------------------------------------------------|--------------------------------|
| `AWS_REGION`          | `us-east-1`                                    | Region AWS                     |
| `DYNAMO_TABLE_NAME`   | `iac-data-store`                               | Nazwa tabeli DynamoDB          |
| `LAMBDA_FUNC_NAME`    | `iac-data-fetcher`                             | Nazwa funkcji Lambda           |
| `LAMBDA_ROLE_NAME`    | `iac-lambda-role`                              | Nazwa roli IAM                 |
| `LOG_GROUP_NAME`      | `/aws/lambda/iac-data-fetcher`                 | CloudWatch Log Group           |
| `SNS_TOPIC_NAME`      | `iac-alerts`                                   | Nazwa tematu SNS               |
| `ALERT_EMAIL`         | *(puste — email wyłączony)*                    | Email dla alertów SNS          |
| `EXTERNAL_API_URL`    | `https://jsonplaceholder.typicode.com/posts`   | URL zewnętrznego API           |

## Co tworzy deployment

```
AWS
├── IAM
│   └── iac-lambda-role
│       ├── AWSLambdaBasicExecutionRole (managed)
│       └── iac-lambda-permissions (inline)
│           ├── dynamodb:PutItem, GetItem, BatchWriteItem, DescribeTable
│           └── cloudwatch:PutMetricData
│
├── DynamoDB
│   └── iac-data-store
│       ├── PartitionKey: id (String)
│       ├── SortKey: timestamp (String)
│       ├── BillingMode: PAY_PER_REQUEST
│       └── PointInTimeRecovery: enabled
│
├── Lambda
│   └── iac-data-fetcher
│       ├── Runtime: Python 3.12
│       ├── Timeout: 60s / Memory: 256MB
│       └── Env: DYNAMODB_TABLE_NAME, EXTERNAL_API_URL, MAX_RETRIES
│
├── CloudWatch
│   ├── Log Group: /aws/lambda/iac-data-fetcher (retencja 30 dni)
│   ├── Alarm: iac-data-fetcher-errors     (≥1 błąd w 5 min)
│   ├── Alarm: iac-data-fetcher-duration   (>45s średnio w 5 min)
│   ├── Alarm: iac-data-fetcher-throttles  (≥1 throttle w 5 min)
│   └── Dashboard: iac-data-fetcher-dashboard
│
└── SNS
    └── iac-alerts (→ email jeśli ALERT_EMAIL ustawiony)
```

## Co robi Lambda (`handler.py`)

1. **Health check** — sprawdza czy tabela DynamoDB jest ACTIVE
2. **Fetch** — pobiera dane z `EXTERNAL_API_URL` z retry (exponential backoff) i obsługą HTTP 429
3. **Validate** — sprawdza wymagane pola: `id`, `title`, `body`, `userId`
4. **Transform** — normalizuje dane, dodaje `timestamp`, `processed_at`, `source_url`
5. **Store** — batch_write do DynamoDB (automatyczny retry nieudanych zapisów)
6. **Metrics** — wysyła `ProcessedRecords`, `ExecutionDurationMs`, `Errors` do CloudWatch namespace `IaC/Lambda`

Wszystkie logi są w formacie JSON (structured logging) dla łatwego parsowania w CloudWatch Insights.

### Przykładowy log Lambda

```json
{"time": "2024-01-15T12:00:00+00:00", "level": "INFO", "step": "fetch", "message": "Pobrano 100 rekordów", "count": 100}
{"time": "2024-01-15T12:00:01+00:00", "level": "INFO", "step": "process", "message": "Przetworzono: 100 poprawnych, 0 odrzuconych", "valid": 100, "invalid": 0}
{"time": "2024-01-15T12:00:03+00:00", "level": "INFO", "step": "store", "message": "Zapisano 100 rekordów", "table": "iac-data-store", "count": 100}
{"time": "2024-01-15T12:00:03+00:00", "level": "INFO", "step": "done", "message": "Zakończono pomyślnie", "stored": 100, "duration_ms": 3241.5}
```

## Schemat tabeli DynamoDB

| Atrybut        | Typ   | Opis                                      |
|----------------|-------|-------------------------------------------|
| `id`           | S     | **Partition Key** — ID z API              |
| `timestamp`    | S     | **Sort Key** — czas zapisu (ISO 8601)     |
| `title`        | S     | Tytuł (max 500 znaków)                    |
| `body`         | S     | Treść (max 2000 znaków)                   |
| `user_id`      | S     | ID użytkownika                            |
| `source_url`   | S     | URL źródłowego API                        |
| `processed_at` | S     | Czas przetworzenia (ISO 8601)             |
