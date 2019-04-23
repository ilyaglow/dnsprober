package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ilyaglow/evio"
	"github.com/miekg/dns"
)

var domainsFile = flag.String("i", "domains.txt", "File with domains")

func main() {
	flag.Parse()
	conn, err := net.DialUDP("udp", nil, &net.UDPAddr{
		IP:   net.ParseIP("8.8.8.8"),
		Port: 53,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	go func() {
		err := doEvio(conn)
		if err != nil {
			log.Println(err)
		}
	}()

	f, err := os.Open(*domainsFile)
	if err != nil {
		log.Fatal(err)
	}

	cancel := make(chan os.Signal, 1)
	signal.Notify(cancel, syscall.SIGINT, syscall.SIGTERM)

	ticker := time.NewTicker(1 * time.Millisecond)

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		select {
		case <-ticker.C:
			m := new(dns.Msg)
			m.SetQuestion(fmt.Sprintf("%s.", scanner.Text()), dns.TypeA)
			b, err := m.Pack()
			if err != nil {
				log.Fatal(err)
			}

			_, err = conn.Write(b)
			if err != nil {
				log.Fatal(err)
			}
		case <-cancel:
			os.Exit(1)
		}
	}

	log.Println("waiting 10 seconds...")
	time.Sleep(10 * time.Second)
}

func doEvio(conn *net.UDPConn) error {
	var events evio.Events
	events.Data = func(c evio.Conn, in []byte) (out []byte, action evio.Action) {
		m := new(dns.Msg)
		err := m.Unpack(in)
		if err != nil {
			log.Println(err)
		}

		fmt.Println(m)
		return
	}
	return evio.ServeUDPConn(events, conn)
}
