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
	Version     = "0.0.10"
	SHM_KEY     = 0x1238
	SHM_SIZE    = 512
	IPC_CREAT   = 01000
	DEFAULT_DEV = "/dev/serial/by-path/platform-20980000.usb-usb-0:1.4:1.0-port0"
)

// Estructura de datos para SHM (v0.0.9)
type ShmPayload struct {
	Timestamp   int64
	GenInv      float64
	Grid        float64
	BattPower   float64
	SOC         float64
	GridCTInt   float64 // Registro 169 (Antes LoadUPS)
	LoadTotal   float64
	TempDisipDC float64
	TempBatt    float64
	PV1Power    float64 // Registro 186
	PV2Power    float64 // Registro 187
	PV3Power    float64 // Registro 188
	PV4Power    float64 // Registro 189
	TempDisipAC float64
	InvOutPower float64 // Registro 175 (NUEVA)
	Padding     [388]byte // Ajustado para mantener 512 bytes totales (412 - 3*8)
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
		data.GenInv      = readU16(client, 166, 1.0)
		data.Grid        = readS16(client, 172, 1.0)
		data.BattPower   = readS16(client, 190, 1.0)
		data.SOC         = readU16(client, 184, 1.0)
		data.GridCTInt   = readS16(client, 169, 1.0) // Registro 169
		data.InvOutPower = readS16(client, 175, 1.0) // Registro 175
		data.LoadTotal   = readU16(client, 178, 1.0)
		data.PV1Power    = readU16(client, 186, 1.0)
		data.PV2Power    = readU16(client, 187, 1.0)
		data.PV3Power    = readU16(client, 188, 1.0)
		data.PV4Power    = readU16(client, 189, 1.0)

		// Temperaturas
		data.TempDisipDC = readU16(client, 90, 0.1) - 100.0
		data.TempDisipAC = readU16(client, 91, 0.1) - 100.0
		data.TempBatt    = readU16(client, 182, 0.1)

		// Cálculo de integridad y escritura en SHM
		data.CRC = calculateCRC(&data)
		writeToSHM(shmAddr, &data)

		totalSolar := data.PV1Power + data.PV2Power + data.PV3Power + data.PV4Power

		// Refresco de pantalla (Terminal)
		fmt.Printf("\033[H\033[2J")
		fmt.Printf("DEYE CONTROL v%s | %s\n", Version, time.Now().Format("15:04:05"))
		fmt.Println("--------------------------------------------------")
		fmt.Printf(" BATERÍA:    %3.0f%%  |  POTENCIA BATT: %6.1f W\n", data.SOC, data.BattPower)
		fmt.Printf(" SOLAR TOTAL:%6.1f W |  GEN-INV:      %6.1f W\n", totalSolar, data.GenInv)
		fmt.Printf(" (PV1:%.0f PV2:%.0f PV3:%.0f PV4:%.0f)\n", data.PV1Power, data.PV2Power, data.PV3Power, data.PV4Power)
		fmt.Printf(" RED/GRID:  %6.1f W |  GRID CT INT:  %6.1f W\n", data.Grid, data.GridCTInt)
		fmt.Printf(" INV OUT:   %6.1f W |  LOAD TOTAL:   %6.1f W\n", data.InvOutPower, data.LoadTotal)
		fmt.Printf(" DISIP DC:  %4.1f °C |  DISIP AC:     %4.1f °C\n", data.TempDisipDC, data.TempDisipAC)
		fmt.Printf(" TEMP BATT: %4.1f °C |\n", data.TempBatt)
		fmt.Println("--------------------------------------------------")
	}
}

// --- Modbus Helpers ---

func readU16(c modbus.Client, addr uint16, scale float64) float64 {
	res, err := c.ReadHoldingRegisters(addr, 1)
	if err != nil { return 0 }
	return float64(binary.BigEndian.Uint16(res)) * scale
}

func readS16(c modbus.Client, addr uint16, scale float64) float64 {
	res, err := c.ReadHoldingRegisters(addr, 1)
	if err != nil { return 0 }
	return float64(int16(binary.BigEndian.Uint16(res))) * scale
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