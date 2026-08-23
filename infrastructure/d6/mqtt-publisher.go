// Command mqtt-publisher is a disposable D6 rehearsal smoke publisher.
// Credentials are deliberately read from protected files rather than flags.
package main

import (
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

func readSecret(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func main() {
	host := flag.String("host", "mqtt", "broker host")
	port := flag.String("port", "8883", "broker port")
	caPath := flag.String("ca-file", "/certs/ca.crt", "CA certificate")
	usernamePath := flag.String("username-file", "/run/poweriot/secrets/mqtt-username", "username file")
	passwordPath := flag.String("password-file", "/run/poweriot/secrets/mqtt-password", "password file")
	topic := flag.String("topic", "device/upload/data", "publish topic")
	message := flag.String("message", "", "publish payload")
	flag.Parse()

	username, err := readSecret(*usernamePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "unable to read MQTT username")
		os.Exit(1)
	}
	password, err := readSecret(*passwordPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "unable to read MQTT password")
		os.Exit(1)
	}
	caPEM, err := os.ReadFile(*caPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "unable to read MQTT CA")
		os.Exit(1)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		fmt.Fprintln(os.Stderr, "unable to parse MQTT CA")
		os.Exit(1)
	}

	tlsConfig := &tls.Config{RootCAs: pool, ServerName: *host, MinVersion: tls.VersionTLS12}
	options := mqtt.NewClientOptions().
		AddBroker("tls://" + *host + ":" + *port).
		SetClientID("d6-rehearsal-smoke").
		SetUsername(username).
		SetPassword(password).
		SetTLSConfig(tlsConfig).
		SetConnectTimeout(10 * time.Second)
	client := mqtt.NewClient(options)
	token := client.Connect()
	if !token.WaitTimeout(15*time.Second) || token.Error() != nil {
		fmt.Fprintln(os.Stderr, "MQTT smoke connection failed")
		os.Exit(1)
	}
	publish := client.Publish(*topic, 0, false, *message)
	if !publish.WaitTimeout(15*time.Second) || publish.Error() != nil {
		fmt.Fprintln(os.Stderr, "MQTT smoke publish failed")
		client.Disconnect(100)
		os.Exit(1)
	}
	client.Disconnect(100)
}
