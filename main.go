package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"strings"
	"time"

	"github.com/jbpratt/stk-memes/internal/node"
	"golang.org/x/crypto/ssh"
)

type cfg struct {
	IdentityFile string         `json:"identity_file"`
	OVHConfig    node.OVHConfig `json:"ovh_config"`
	STKUsername  string         `json:"stk_username"`
	STKPassword  string         `json:"stk_password"`
}

func main() {
	ctx := context.Background()

	cfgPath := flag.String("path", "", "path to config file")
	flag.Parse()

	file, err := os.Open(*cfgPath)
	if err != nil {
		log.Fatalln("failed to open cfg file:", cfgPath, err)
	}

	contents, err := ioutil.ReadAll(file)
	if err != nil {
		log.Fatalln("failed to read cfg file:", err)
	}

	config := &cfg{}
	if err := json.Unmarshal(contents, config); err != nil {
		log.Fatalln("failed to unmarshal cfg contents:", err)
	}

	d, err := ioutil.ReadFile(config.IdentityFile + ".pub")
	if err != nil {
		log.Fatalln("error reading ssh public key", err)
	}
	pubkey := string(bytes.Trim(d, "\r\n\t "))

	driver, err := node.NewOVHDriver(
		"CA",
		config.OVHConfig.AppKey,
		config.OVHConfig.AppSecret,
		config.OVHConfig.ConsumerKey,
		config.OVHConfig.ProjectID,
	)
	if err != nil {
		log.Fatalln(err)
	}

	req := &node.CreateRequest{
		User:        driver.DefaultUser(),
		Name:        "stk-memes",
		Region:      "BHS5",
		SKU:         "B2-15",
		SSHKey:      pubkey,
		BillingType: node.Hourly,
	}

	log.Println("creating node")

	n, err := driver.Create(ctx, req)
	if err != nil {
		log.Fatalln(req, err)
	}

	log.Println("node created")

	log.Println("sleeping for node creation")
	time.Sleep(1 * time.Minute)

	privkey, err := ioutil.ReadFile(config.IdentityFile)
	if err != nil {
		log.Fatalln("failed to read priv key: %w", err)
	}

	signer, err := ssh.ParsePrivateKey(privkey)
	if err != nil {
		log.Fatalln("failed to parse private key: %w", err)
	}

	conf := &ssh.ClientConfig{
		User:            driver.DefaultUser(),
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         time.Duration(10 * time.Second),
	}

	log.Println("sshing to the server")

	conn, err := ssh.Dial("tcp", fmt.Sprintf("%s:22", n.Networks.V4[0]), conf)
	if err != nil {
		log.Fatalln("failed to dial node: %w", err)
	}

	session, err := conn.NewSession()
	if err != nil {
		log.Fatalln("failed to create ssh session: %w", err)
	}
	defer session.Close()

	stdin, err := session.StdinPipe()
	if err != nil {
		log.Fatalln("failed to create stdin: %w", err)
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		log.Fatalln("failed to create stderr: %w", err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		log.Fatalln("failed to create stdout: %w", err)
	}

	copy := func(w io.Writer, r io.Reader) {
		if _, err := io.Copy(w, r); err != nil {
			log.Printf("error while copying: %s\n", err.Error())
		}
	}

	go copy(os.Stderr, stderr)
	go copy(os.Stdout, stdout)

	log.Println("connected.. running commands")

	cmds := [][]string{
		{"sudo", "apt-get", "-y", "update"},
		{"sudo", "apt-get", "-y", "upgrade"},
		{
			"sudo", "apt-get", "-y", "install",
			"build-essential",
			"subversion",
			"cmake",
			"libbluetooth-dev",
			"libsdl2-dev",
			"libcurl4-openssl-dev",
			"libenet-dev",
			"libfreetype6-dev",
			"libharfbuzz-dev",
			"libjpeg-dev",
			"libogg-dev",
			"libopenal-dev",
			"libpng-dev",
			"libssl-dev",
			"libvorbis-dev",
			"nettle-dev",
			"pkg-config",
			"zlib1g-dev",
		},
		{"mkdir", "stk"},
		{"cd", "stk"},
		{"git", "clone", "https://github.com/supertuxkart/stk-code", "stk-code"},
		{"svn", "co", "https://svn.code.sf.net/p/supertuxkart/code/stk-assets", "stk-assets"},
		{"cd", "stk-code"},
		{"mkdir", "cmake_build"},
		{"cd", "cmake_build"},
		{"cmake", "..", "-DSERVER_ONLY=ON"},
		{"sudo", "make", "install"},
		{"supertuxkart", "--init-user", fmt.Sprintf("--login=%s", config.STKUsername), fmt.Sprintf("--password=%s", config.STKPassword)},
		{
			"wget",
			"https://gist.githubusercontent.com/jbpratt/e31529a43e274cb5b1af3234169a98c4/raw/bc9cb9d743be44b748aeaf0cf89ed915751669d1/stk-conf",
			"-O", "$HOME/.config/supertuxkart/config-0.10/server_config.xml",
		},
		{"sudo", "ufw", "allow", "2759"},
	}

	if err := session.Shell(); err != nil {
		log.Fatalln("failed to start ssh shell: %w", err)
	}

	// throw all the commands into the buffer then wait
	for _, cmd := range cmds {
		cmdstr := strings.Join(cmd, " ")
		log.Printf("+ %q\n", cmdstr)
		_, err = fmt.Fprintf(stdin, "%s\n", cmdstr)
		if err != nil {
			log.Fatalln("failed to write command: %w", err)
		}
	}

	if err := session.Wait(); err != nil {
		log.Fatalln(err)
	}
}
