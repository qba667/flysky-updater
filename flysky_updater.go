package main

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"github.com/cheggaaa/pb"
	"github.com/tarm/serial"
	"gopkg.in/alecthomas/kingpin.v2"
	"io/ioutil"
	"time"
)

var (
	//port     = kingpin.Arg("port", "port").Required().String()
	//filename = kingpin.Arg("filename", "filename").Required().String()
	//force    = kingpin.Flag("force", "Force flash.").Short('f').Bool()
	verbose  = kingpin.Flag("verbose", "Verbose mode.").Short('v').Bool()
)

func make_checksum(payload []byte) []byte {
	var checksum uint16 = 0xFFFF
	for i := 0; i < len(payload); i++ {
		checksum -= uint16(payload[i])
	}
	ret := make([]byte, 2)
	binary.LittleEndian.PutUint16(ret, checksum)
	return ret
}

func WriteAll(s *serial.Port, raw []byte) error {
	if *verbose {
		fmt.Println("Write", raw[:2], raw[2:len(raw)-2], raw[len(raw)-2:])
	}
	n, err := s.Write(raw)
	if err != nil {
		return err
	}
	if n != len(raw) {
		return errors.New("Didn't write all bytes.")
	}
	return nil
}

func WriteFrame(s *serial.Port, payload []byte) error {
	length := make([]byte, 2)
	binary.LittleEndian.PutUint16(length, uint16(len(payload)+4))
	frame := append(length, payload...)
	frame = append(frame, make_checksum(frame)...)
	return WriteAll(s, frame)
}

func ReadAll(s *serial.Port, n int) ([]byte, error) {
	buf := make([]byte, n)
	bytes_read := 0
	for bytes_read < n {
		c, err := s.Read(buf[bytes_read:])
		if err != nil {
			return nil, err
		}
		if c == 0 {
			return nil, errors.New("Read timeout")
		}
		bytes_read += c
	}
	return buf, nil
}

func EmptyRx(s *serial.Port) {
	c := 1
	buf := make([]byte, 1024)
	for c > 0 {
		c, _ = s.Read(buf)
	}
}

func ReadFrame(s *serial.Port) ([]byte, error) {
	head, err := ReadAll(s, 3)
	if err != nil {
		return nil, err
	}
	if head[0] != 0x55 {
		return nil, errors.New("Invalid response")
	}
	size := int(binary.LittleEndian.Uint16(head[1:]))
	body, err := ReadAll(s, size-3)
	if err != nil {
		return nil, err
	}
	payload := body[:len(body)-2]
	checksum := body[len(body)-2:]
	if *verbose {
		fmt.Println("Read", head, payload, checksum)
	}
	checksum_cmp := make_checksum(append(head, payload...))
	if !bytes.Equal(checksum, checksum_cmp) {
		return nil, errors.New("Invalid checksum")
	}
	return payload, nil
}

func ping(s *serial.Port) ([]byte, error) {
	err := WriteFrame(s, []byte{0xC0})
	if err != nil {
		return nil, err
	}
	answer, err := ReadFrame(s)
	if err != nil {
		return nil, err
	}
	if answer[0] != 0xC0 {
		return nil, errors.New("Unexpected answer to ping")
	}
	return answer, nil
}

func communicate(s *serial.Port, request []byte, response []byte) error {
	err := WriteFrame(s, request)
	if err != nil {
		return err
	}
	msg, err := ReadFrame(s)
	if err != nil {
		return err
	}
	if !bytes.Equal(msg, response) {
		errors.New("Unexpected response: " + hex.Dump(response))
	}
	return nil
}

func ask_write(s *serial.Port, address int) error {
	ask_permission := []byte{0xc2, 0x00, 0x00, 0x00, 0x09, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	binary.LittleEndian.PutUint16(ask_permission[1:3], uint16(address))
	get_permission := []byte{0xc2, 0x80, 0x00, 0x00, 0x00, 0x09, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	binary.LittleEndian.PutUint16(get_permission[2:4], uint16(address))

	return communicate(s, ask_permission, get_permission)
}

func write_chunk(s *serial.Port, address int, data []byte) error {
	write_instruction := []byte{0xc3, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01}
	binary.LittleEndian.PutUint16(write_instruction[1:3], uint16(address))
	write_instruction = append(write_instruction, data...)
	write_confirmation := []byte{0xc3, 0x00, 0x00, 0x00, 0x00}

	return communicate(s, write_instruction, write_confirmation)
}

func update(s *serial.Port, firmware []byte) error {
	start_address := 0x1800

	bar := pb.New(len(firmware)).SetUnits(pb.U_BYTES)
	bar.Start()

	for bytes_written := 0; bytes_written < len(firmware); bytes_written += 1024 {
		tries := 0
	ask:
		err := ask_write(s, start_address+bytes_written)
		if err != nil {
			tries++
			if tries <= 3 {
				EmptyRx(s)
				goto ask
			}
		}
		for chunk := 0; chunk < 1024; chunk += 256 {
			offset := bytes_written+chunk;
			end := offset + 256;
						
			err = write_chunk(s, start_address+offset, firmware[offset:end])
			if err != nil {
				tries++
				if tries <= 3 {
					EmptyRx(s)
					goto ask
				}
			}
			bar.Add(256)
		}
	}

	bar.FinishPrint("Upload completed.")
	return nil
}


func restart(s *serial.Port) error {
	return WriteFrame(s, []byte{0xC1, 0x00})
}

func main() {
	//kingpin.Parse()

	fmt.Println(" _______ ___    __   __ _______ ___   _ __   __   ___ ___     ");
	fmt.Println("|       |   |  |  | |  |       |   | | |  | |  | |   |   |    ");
	fmt.Println("|    ___|   |  |  |_|  |  _____|   |_| |  |_|  | |   |   |___ ");
	fmt.Println("|   |___|   |  |       | |_____|      _|       | |   |    _  |");
	fmt.Println("|    ___|   |__|_     _|_____  |     |_|_     _| |   |   | | |");
	fmt.Println("|   |   |       ||   |  _____| |    _  | |   |   |   |   |_| |");
	fmt.Println("|___|   |_______||___| |_______|___| |_| |___|   |___|_______|");
	fmt.Println("\r\n\r\nUpdater by mhils\r\nhttps://github.com/mhils/flysky-updater\r\n\r\n");
	for {
	binFiles, _ := filepath.Glob("./*.bin")
	
	if(len(binFiles) <=0) {
		panic("No firmware found!!!")
	}

	var comports []int;
	for p := 0; p < 24; p += 1 {
		var portStr string = fmt.Sprintf("COM%d", p)
		c := &serial.Config{Name: portStr, Baud: 115200, ReadTimeout: time.Second * 1}
		s, err := serial.OpenPort(c)
		if err != nil {
			continue
		}
		s.Close()
		comports = append(comports, p)
		
	}
	if(len(comports) <=0) {
		fmt.Println("No serial ports found!!!");
		 time.Sleep(5000 * time.Millisecond);
		continue;
	}
	var portIndex int
	if(len(comports) == 1){
		portIndex = comports[0];
	} else{
		for p := 0; p < len(comports); p += 1 {
			fmt.Println(fmt.Sprintf("[%d] COM%d", comports[p], comports[p]));
		}
		fmt.Print("Please select serial port: ");
		fmt.Scan(&portIndex)
	}

	fmt.Println(fmt.Sprintf("Selected serial port: COM %d", portIndex));
	
	var fwNames []string;
	var firmwareFile int;
	
	
	for f := 0; f < len(binFiles); f += 1 {
		content, err := ioutil.ReadFile(binFiles[f])
		if err != nil {
			continue;
		}
		if(len(content) < 0xE700){
			continue;
		}
		var fwAddress int = 0xD6AD;
		var fwdate int = 0xD6C0;
		if(len(content) > 0xE7FF){
			fwAddress += 0x1800;
			fwdate += 0x1800;
		}
		
		fw := string(content[fwAddress:fwAddress+16]);
		date := string(content[fwdate:fwdate+16]);
		
		fwNames = append(fwNames, fw +" " + date);
		
		fmt.Println(fmt.Sprintf("[%d] %s %d bytes %s %s", f, binFiles[f], len(content), fw, date));
	}
	if(len(binFiles) == 1){
		firmwareFile = 0;
	} else {
		
		fmt.Print("Please select firmware: ");
		fmt.Scan(&firmwareFile)
	}
	
	fmt.Println(fmt.Sprintf("Selected firmware: %s", fwNames[firmwareFile]));

	raw, err := ioutil.ReadFile(binFiles[firmwareFile])
	if err != nil {
		fmt.Println(err)
		continue;
	}
	var data []byte;
	
	fmt.Println(fmt.Sprintf("File size: %d", len(raw)));
	if len(raw) > 0xE7FF {
        data = make([]byte, len(raw)-0x1800);
		copy(data, raw[1800:len(data)]);
    } else {
        data = make([]byte, len(raw));
		copy(data, raw[0:len(data)]);
    }
	
	fmt.Println(fmt.Sprintf("Data size: %d", len(data)));
	if (len(data) < 0x9000 || len(data) > 0xe7ff) {
		fmt.Println(fmt.Sprintf("Unexpected firmare size: %d bytes", len(data)));
		continue;
	}

	c := &serial.Config{Name: fmt.Sprintf("COM%d", portIndex), Baud: 115200, ReadTimeout: time.Second * 1}
	s, err := serial.OpenPort(c)
	if err != nil {
		fmt.Println(err);
		s.Close()
		continue;
	}

	_, err = ping(s)
	if err != nil {
		fmt.Println(err);
		s.Close()
		continue;
	}

	err = update(s, data)
	if err != nil {
		fmt.Println(err);
		s.Close()
		continue;
	}

	err = restart(s)
	if err != nil {
		fmt.Println(err);
		s.Close()
		continue;
	}
	s.Close()
	break;
	}
	fmt.Println("Success!")
	
}
