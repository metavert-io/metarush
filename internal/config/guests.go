package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// The guest listener (D491): a second web server of its own, for guests only
// (people outside the workspace an owner shares versions with). It listens on
// 127.0.0.1 alone, never another address: this computer may sit on a shared
// network, and anything beyond this computer reaches it only through what a
// later release puts in front of it. It is off unless a person turns guests
// on at this computer (`guests.listen`), at guests.port (DefaultGuestPort).
// Like remote access it changes while MetaRush runs, so code reads it through
// Config.GuestListener.

// DefaultGuestPort is where the guest listener answers unless config.yaml says
// another port.
const DefaultGuestPort = 4846

// GuestHost is the only address the guest listener binds: loopback, never
// 0.0.0.0, :: or a network's address.
const GuestHost = "127.0.0.1"

// Guests is this computer's guest listener in config.yaml.
type Guests struct {
	// Listen: this computer answers guests (off unless a person turned guests
	// on here).
	Listen bool `yaml:"listen,omitempty"`
	// Port is where it listens on 127.0.0.1 (0: DefaultGuestPort).
	Port int `yaml:"port,omitempty"`
}

// PortOrDefault is the port the listener uses.
func (g Guests) PortOrDefault() int {
	if g.Port > 0 {
		return g.Port
	}
	return DefaultGuestPort
}

// Address is the guest address: where guests' browsers reach the listener.
// Nothing outside this computer can reach it in this release, so it is
// always this computer's own address at the listener's port.
func (g Guests) Address() string {
	return fmt.Sprintf("http://%s:%d", GuestHost, g.PortOrDefault())
}

// CheckGuestPort says whether a port can be the guest listener's: a port
// people's programs use (1024–65535) that isn't the dashboard's.
func CheckGuestPort(port, dashboard int) error {
	switch {
	case port < 1024 || port > 65535:
		return fmt.Errorf("a port from 1024 to 65535")
	case port == dashboard:
		return fmt.Errorf("another port than the dashboard's (%d)", dashboard)
	}
	return nil
}

// GuestListener returns the guest listener's settings in effect.
func (c *Config) GuestListener() Guests {
	if c.live != nil {
		if p := c.live.guests.Load(); p != nil {
			return *p
		}
	}
	return c.Guests
}

// SetGuestListener changes them for everything sharing this config: the
// daemon starts, moves or stops the listener.
func (c *Config) SetGuestListener(g Guests) {
	c.ensureLive()
	c.live.guests.Store(&g)
}

// LoadGuests reads only the guests section of <home>/config.yaml (with
// ${VAR:default} expansion, resolving no secrets).
func LoadGuests(home string) (Guests, error) {
	raw, err := os.ReadFile(Path(home))
	if err != nil {
		return Guests{}, err
	}
	var only struct {
		Guests Guests `yaml:"guests"`
	}
	if err := yaml.Unmarshal([]byte(Expand(string(raw))), &only); err != nil {
		return Guests{}, err
	}
	return only.Guests, nil
}
