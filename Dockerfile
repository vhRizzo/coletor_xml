# --- Estágio 1: Build ---
# Usa uma imagem oficial do Go para compilar a aplicação
FROM golang:1.24-alpine AS builder

# Define o diretório de trabalho dentro do contêiner
WORKDIR /app

# Copia os arquivos de dependência e baixa os pacotes
COPY go.mod go.sum ./
RUN go mod download

# Copia todo o código fonte da sua aplicação
COPY . .

# Compila a aplicação. As flags criam um binário estático.
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o coletor_xml .


# --- Estágio 2: Produção ---
# Usa uma imagem "do zero" (scratch) que é minúscula e segura,
# pois não contém nada além do seu programa.
FROM scratch

# Copia o binário compilado do estágio de build
COPY --from=builder /app/coletor_xml /

# Define o comando que será executado quando o contêiner iniciar
CMD ["/coletor_xml"]