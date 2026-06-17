# Curl Commands — Items API

## List all items

```bash
curl http://localhost:8000/items
```

## Get a single item

```bash
curl http://localhost:8000/items/1
```

## Create an item

```bash
curl -X POST http://localhost:8000/items \
  -H "Content-Type: application/json" \
  -d '{"name": "Test item", "value": "123"}'
```

## Update an item

```bash
curl -X PUT http://localhost:8000/items/1 \
  -H "Content-Type: application/json" \
  -d '{"name": "Updated name", "value": "456"}'
```

## Delete an item

```bash
curl -X DELETE http://localhost:8000/items/1
```

---

# Curl Commands — Auth API

## Sign up

```bash
curl -X POST http://localhost:8000/auth/signup \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Test User",
    "email": "test@example.com",
    "password": "password123"
  }'
```

## Login (seeded dev user)

```bash
curl -X POST http://localhost:8000/auth/login \
  -H "Content-Type: application/json" \
  -d '{
    "email": "foo@bar.com",
    "password": "password"
  }'
```

## Get current user (replace JWT_TOKEN)

```bash
curl http://localhost:8000/auth/me \
  -H "Authorization: Bearer JWT_TOKEN"
```

---

# Curl Commands — Transactions API

## Create a transaction

```bash
curl -X POST http://localhost:8000/api/transactions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer JWT_TOKEN" \
  -d '{
    "amount": -42.50,
    "merchant_name": "Trader Joes",
    "merchant_category": "supermarket",
    "spending_category": "groceries",
    "transaction_type": "debit",
    "payment_method": "debit_card",
    "city": "Berkeley",
    "country": "US",
    "currency": "USD",
    "description": "Weekly grocery run"
  }'
```

## Fetch transactions by spending category

```bash
curl http://localhost:8000/api/transactions/category/groceries \
  -H "Authorization: Bearer JWT_TOKEN"
```

## Fetch current month transactions

```bash
curl http://localhost:8000/api/transactions/current-month \
  -H "Authorization: Bearer JWT_TOKEN"
```

## Fetch previous month transactions

```bash
curl http://localhost:8000/api/transactions/previous-month \
  -H "Authorization: Bearer JWT_TOKEN"
```

## Fetch N most recent transactions (default 10)

```bash
curl "http://localhost:8000/api/transactions/recent?limit=5" \
  -H "Authorization: Bearer JWT_TOKEN"
```
