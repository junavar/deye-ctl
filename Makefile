# Variables de despliegue
RPI_USER = pi
RPI_IP   = 192.168.1.153
RPI_DEST = /home/pi/

BINARY_NAME = deye-ctl-arm6
GO_VARS = GOOS=linux GOARCH=arm GOARM=6 GO111MODULE=on

.PHONY: all build deploy clean

all: build

build:
	@echo "Compilando $(BINARY_NAME) para ARMv6..."
	$(GO_VARS) go build -o $(BINARY_NAME) .

deploy: build
	@echo "Copiando binario a la Raspberry Pi..."
	scp $(BINARY_NAME) $(RPI_USER)@$(RPI_IP):$(RPI_DEST)
	@echo "Despliegue de deye-ctl completado."

clean:
	rm -f $(BINARY_NAME)
	@echo "Limpieza completada."