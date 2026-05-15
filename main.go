package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"hash/crc32"
	"log"
	"syscall"
	"time"
	"unsafe"

	"github.com/goburrow/modbus"
)

const (
	Version     = "0.0.11"
	SHM_KEY     = 0x1238
	SHM_SIZE    = 512
	IPC_CREAT   = 01000
	DEFAULT_DEV = "/dev/serial/by-path/platform-20980000.usb-usb-0:1.4:1.0-port0"
)

// Estructura de datos para SHM (v0.0.11
type ShmPayload struct {
	Timestamp   int64
	PV1Power      float32 // Registro 186
	PV2Power      float32 // Registro 187
	PV3Power      float32 // Registro 188
	PV4Power      float32 // Registro 189
	PVTotalPower  float32 // Calculada: suma PV1..PV4
	BattPower     float32 // Registro 190
	InverterPower float32 // Registro 175
	GenInv        float32 // Registro 166
	GridCTInt     float32 // Registro 169
	GridCTExt     float32 // Registro 172
	LoadTotal     float32 // Calculada: P175+P166+P172
	LoadUPS       float32 // Calculada: P175+P166+P169
	LoadNUPS      float32 // Calculada: P172-P169
	TempDisipDC   float32 // Registro 90
	TempDisipAC   float32 // Registro 91
	TempBatt      float32 // Registro 182
	SOC           float32 // Registro 184
	Padding       [432]byte // Ajustado para 512 bytes totales (512 - 8 - 17*4 - 4)
	CRC         uint32
}

func main() {
	// Argumentos
	portPtr := flag.String("dev", DEFAULT_DEV, "Puerto serial del inversor")
	flag.Parse()

	// Pintar inicio de servicio
	fmt.Printf("Deye Control v%s\n", Version)
	fmt.Printf("Dispositivo: %s\n", *portPtr)
	fmt.Println("----------------------------------------")

	handler := modbus.NewRTUClientHandler(*portPtr)
	handler.BaudRate = 9600
	handler.DataBits = 8
	handler.Parity = "N"
	handler.StopBits = 1
	handler.SlaveId = 1
	handler.Timeout = 1 * time.Second

	if err := handler.Connect(); err != nil {
		log.Fatalf("Error de conexión: %v", err)
	}
	defer handler.Close()
	client := modbus.NewClient(handler)

	// Inicializar SHM
	shmAddr := setupSHM(SHM_KEY, SHM_SIZE)

	ticker := time.NewTicker(1 * time.Second)
	for range ticker.C {
		data := ShmPayload{
			Timestamp: time.Now().Unix(),
		}

		// Lectura de registros Modbus
		data.PV1Power    = readU16(client, 186, 1.0)
		data.PV2Power    = readU16(client, 187, 1.0)
		data.PV3Power    = readU16(client, 188, 1.0)
		data.PV4Power    = readU16(client, 189, 1.0)
		data.BattPower   = readS16(client, 190, 1.0)
		data.InverterPower = readS16(client, 175, 1.0)
		data.GenInv      = readU16(client, 166, 1.0)
		data.GridCTInt   = readS16(client, 169, 1.0)
		data.GridCTExt   = readS16(client, 172, 1.0)
		data.SOC         = readU16(client, 184, 1.0)

		// Cálculos de potencia
		data.PVTotalPower = data.PV1Power + data.PV2Power + data.PV3Power + data.PV4Power
		data.LoadTotal    = data.InverterPower + data.GenInv + data.GridCTExt
		data.LoadUPS      = data.InverterPower + data.GenInv + data.GridCTInt
		data.LoadNUPS     = data.GridCTExt - data.GridCTInt

		// Temperaturas (Escala 0.1 y offset en disipadores)
		data.TempDisipDC = readU16(client, 90, 0.1) - 100.0
		data.TempDisipAC = readU16(client, 91, 0.1) - 100.0
		data.TempBatt    = readU16(client, 182, 0.1)

		// Cálculo de integridad y escritura en SHM
		data.CRC = calculateCRC(&data)
		writeToSHM(shmAddr, &data)

		// Refresco de pantalla (Terminal)
		fmt.Printf("\033[H\033[2J")
		fmt.Printf("DEYE CONTROL v%s | %s\n", Version, time.Now().Format("15:04:05"))
		fmt.Println("--------------------------------------------------")
		fmt.Printf(" BATERÍA:    %3.0f%%  |  POTENCIA BATT: %6.1f W\n", data.SOC, data.BattPower)
		fmt.Printf(" SOLAR TOTAL:%6.1f W |  GEN-INV:      %6.1f W\n", data.PVTotalPower, data.GenInv)
		fmt.Printf(" (PV1:%.0f PV2:%.0f PV3:%.0f PV4:%.0f)\n", data.PV1Power, data.PV2Power, data.PV3Power, data.PV4Power)
		fmt.Printf(" GRID CT EXT:%6.1f W |  GRID CT INT:  %6.1f W\n", data.GridCTExt, data.GridCTInt)
		fmt.Printf(" INV POWER: %6.1f W |  LOAD TOTAL:   %6.1f W\n", data.InverterPower, data.LoadTotal)
		fmt.Printf(" LOAD UPS:  %6.1f W |  LOAD NUPS:    %6.1f W\n", data.LoadUPS, data.LoadNUPS)
		fmt.Printf(" DISIP DC:  %4.1f °C |  DISIP AC:     %4.1f °C\n", data.TempDisipDC, data.TempDisipAC)
		fmt.Printf(" TEMP BATT: %4.1f °C |\n", data.TempBatt)
		fmt.Println("--------------------------------------------------")
	}
}

// --- Modbus Helpers ---

func readU16(c modbus.Client, addr uint16, scale float32) float32 {
	res, err := c.ReadHoldingRegisters(addr, 1)
	if err != nil { return 0 }
	return float32(binary.BigEndian.Uint16(res)) * scale
}

func readS16(c modbus.Client, addr uint16, scale float32) float32 {
	res, err := c.ReadHoldingRegisters(addr, 1)
	if err != nil { return 0 }
	return float32(int16(binary.BigEndian.Uint16(res))) * scale
}

// --- SHM Functions ---

func setupSHM(key, size int) uintptr {
	shmid, _, err := syscall.Syscall(syscall.SYS_SHMGET, uintptr(key), uintptr(size), IPC_CREAT|0666)
	if err != 0 {
		log.Fatalf("shmget error: %v", err)
	}
	addr, _, err := syscall.Syscall(syscall.SYS_SHMAT, shmid, 0, 0)
	if err != 0 {
		log.Fatalf("shmat error: %v", err)
	}
	return addr
}

func writeToSHM(addr uintptr, data *ShmPayload) {
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, data)
	b := buf.Bytes()
	for i := 0; i < len(b); i++ {
		*(*byte)(unsafe.Pointer(addr + uintptr(i))) = b[i]
	}
}

func calculateCRC(data *ShmPayload) uint32 {
	copyData := *data
	copyData.CRC = 0
	var buf bytes.Buffer
	binary.Write(&buf, binary.LittleEndian, copyData)
	// Calculamos sobre los primeros 508 bytes (total struct menos el campo CRC)
	return crc32.ChecksumIEEE(buf.Bytes()[:508])
}