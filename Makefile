.PHONY: all client server

all: client server
	@echo "Built both client and server successfully."

client:
	@echo "Building client..."
	@go build -o mcpClient ./client
	@echo "Client build complete."

server:
	@echo "Building server..."
	@go build -o mcpserver ./server
	@echo "Server build complete."
