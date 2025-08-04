# Build stage
FROM golang:1.24.4-alpine AS builder

# CGO precisa estar ativado para sqlite3
ENV CGO_ENABLED=0

# Instalar libc para CGO funcionar (alpine é musl-based, precisa dessas libs)
RUN apk add --no-cache gcc musl-dev curl

WORKDIR /app

# Copiar dependências primeiro
COPY go.mod go.sum ./
RUN go mod download

# Copiar o restante do código
COPY . .

# Build do binário
RUN go build -o app .

# Runtime stage
FROM alpine:latest

# Runtime precisa de libc também, senão o binário com CGO quebra
RUN apk add --no-cache libc6-compat

WORKDIR /app

# Copiar o binário
COPY --from=builder /app/app .

# Garantir permissão de execução
RUN chmod +x ./app

EXPOSE 8080

CMD ["./app"]
