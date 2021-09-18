package main

import (
	"encoding/json"
	"flag"
	"io"
    "io/ioutil"
	"net/http"

	// "os"
	// "os/exec"
	"strings"
	// "syscall"
	// "unsafe"
	"bytes"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	log "github.com/sirupsen/logrus"

	// "github.com/creack/pty"
	"golang.org/x/crypto/ssh"
)

type windowSize struct {
	Rows uint16 `json:"rows"`
	Cols uint16 `json:"cols"`
	X    uint16
	Y    uint16
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

func connectToHost(user, host string) (*ssh.Client, *ssh.Session, error) {
	// var pass string
	// fmt.Print("Password: ")
	// fmt.Scanf("%s\n", &pass)
    authorizedKeysBytes, _ := ioutil.ReadFile("/home/joe/.ssh/id_rsa.lilly.pub")
    pcert, _, _, _, err := ssh.ParseAuthorizedKey(authorizedKeysBytes)

    privkeyBytes, _ := ioutil.ReadFile("/home/joe/.ssh/id_rsa.lilly")
    upkey, err := ssh.ParseRawPrivateKey(privkeyBytes)

    if err != nil {
        log.Printf("Failed to load authorized_keys, err: %v", err)
    }

    usigner, err := ssh.NewSignerFromKey(upkey)
    if err != nil {
        log.Printf("Failed to create new signer, err: %v", err)
    }
    log.Printf("signer: %v", usigner)

    ucertSigner, err := ssh.NewCertSigner(pcert.(*ssh.Certificate), usigner)

    // key, err := ioutil.ReadFile("/home/joe/.ssh/id_rsa.sophie")
	// if err != nil {
		// log.Fatalf("unable to read private key: %v", err)
	// }

    // // // Create the Signer for this private key.
	// signer, err := ssh.ParsePrivateKey(key)
	// if err != nil {
		// log.Fatalf("unable to parse private key: %v", err)
	// }

	sshConfig := &ssh.ClientConfig{
		User: user,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(ucertSigner)},

	}
	sshConfig.HostKeyCallback = ssh.InsecureIgnoreHostKey()

	client, err := ssh.Dial("tcp", host, sshConfig)
	if err != nil {
		return nil, nil, err
	}

	session, err := client.NewSession()
	if err != nil {
		client.Close()
		return nil, nil, err
	}

	return client, session, nil
}

func StreamToByte(stream io.Reader) []byte {
	buf := new(bytes.Buffer)
	buf.ReadFrom(stream)
	return buf.Bytes()
}

func handleWebsocket(w http.ResponseWriter, r *http.Request) {
	l := log.WithField("remoteaddr", r.RemoteAddr)
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		l.WithError(err).Error("Unable to upgrade connection")
		return
	}

	// cmd := exec.Command("/bin/bash", "-l")
	// cmd.Env = append(os.Environ(), "TERM=xterm")

	// tty, err := pty.Start(cmd)

	//client, session, err := connectToHost("root", "147.75.74.46:22")

	client, session, err := connectToHost("rivs", "127.0.0.1:34213")
	if err != nil {
		l.WithError(err).Error("Unable to start pty/cmd")
		conn.WriteMessage(websocket.TextMessage, []byte(err.Error()))
		return
	}

	// Set up terminal modes
	modes := ssh.TerminalModes{
		ssh.ECHO:          0,     // disable echoing
		ssh.TTY_OP_ISPEED: 14400, // input speed = 14.4kbaud
		ssh.TTY_OP_OSPEED: 14400, // output speed = 14.4kbaud
	}

	defer func() {
		// cmd.Process.Kill()
		// cmd.Process.Wait()
		// tty.Close()
		client.Close()
		conn.Close()
	}()

	stdoutReader, err := session.StdoutPipe()
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte(err.Error()))
		l.WithError(err).Error("Unable to read from pty/cmd")
		return
	}

	go func() {
		buf := make([]byte, 1024)
        for {
		read, err := stdoutReader.Read(buf)
		if err != nil {
			conn.WriteMessage(websocket.TextMessage, []byte(err.Error()))
			l.WithError(err).Error("Unable to read from pty/cmd")
			return
		}
		conn.WriteMessage(websocket.BinaryMessage, buf[:read])
    }

	}()

	stdin, err := session.StdinPipe()

	// Request pseudo terminal
	if err := session.RequestPty("xterm", 80, 40, modes); err != nil {
		log.Fatalf("request for pseudo terminal failed: %s", err)
	}
	// Start remote shell
	if err := session.Shell(); err != nil {
		log.Fatalf("failed to start shell: %s", err)
	}

	// if err := session.Run("ls -lah"); err != nil {
	// 	log.Fatalf("failed to start shell: %s", err)
	// }

	// if err := session.Wait(); err != nil {
	// 	log.Fatalf("failed to wait: %s", err)
	// }

	if err != nil {
		log.Fatalf("Unable to setup stdin for session: %v\n", err)
	}
	for {
		messageType, reader, err := conn.NextReader()
		if err != nil {
			l.WithError(err).Error("Unable to grab next reader")
			return
		}

		if messageType == websocket.TextMessage {
			l.Warn("Unexpected text message")
			conn.WriteMessage(websocket.TextMessage, []byte("Unexpected text message"))
			continue
		}

		dataTypeBuf := make([]byte, 1)
		read, err := reader.Read(dataTypeBuf)
		if err != nil {
			l.WithError(err).Error("Unable to read message type from reader")
			conn.WriteMessage(websocket.TextMessage, []byte("Unable to read message type from reader"))
			return
		}

		if read != 1 {
			l.WithField("bytes", read).Error("Unexpected number of bytes read")
			return
		}

		switch dataTypeBuf[0] {
		case 0:
			log.Info("wtf", string(read))
			copied, err := io.Copy(stdin, reader)
			if err != nil {
				l.WithError(err).Errorf("Error after copying %d bytes", copied)
			}
		case 1:
			decoder := json.NewDecoder(reader)
			resizeMessage := windowSize{}
			err := decoder.Decode(&resizeMessage)
			if err != nil {
				conn.WriteMessage(websocket.TextMessage, []byte("Error decoding resize message: "+err.Error()))
				continue
			}
			log.WithField("resizeMessage", resizeMessage).Info("Resizing terminal")

	        if err := session.WindowChange(int(resizeMessage.Rows), int(resizeMessage.Cols)); err != nil {
                log.Error("Error resizing window", err)
            }

			// _, _, errno := syscall.Syscall(
			// 	syscall.SYS_IOCTL,
			// 	// tty.Fd(),
			// 	syscall.TIOCSWINSZ,
			// 	uintptr(unsafe.Pointer(&resizeMessage)),
			// )
			// if errno != 0 {
			// 	l.WithError(syscall.Errno(errno)).Error("Unable to resize terminal")
			// }
		default:
			l.WithField("dataType", dataTypeBuf[0]).Error("Unknown data type")
		}
	}
}

func main() {
	var listen = flag.String("listen", "127.0.0.1:8080", "Host:port to listen on")
	var assetsPath = flag.String("assets", "./assets", "Path to assets")

	flag.Parse()

	r := mux.NewRouter()

	r.HandleFunc("/term", handleWebsocket)
	r.PathPrefix("/").Handler(http.FileServer(http.Dir(*assetsPath)))

	log.Info("Demo Websocket/Xterm terminal")
	log.Warn("Warning, this is a completely insecure daemon that permits anyone to connect and control your computer, please don't run this anywhere")

	if !(strings.HasPrefix(*listen, "127.0.0.1") || strings.HasPrefix(*listen, "localhost")) {
		log.Warn("Danger Will Robinson - This program has no security built in and should not be exposed beyond localhost, you've been warned")
	}

	if err := http.ListenAndServe(*listen, r); err != nil {
		log.WithError(err).Fatal("Something went wrong with the webserver")
	}
}
