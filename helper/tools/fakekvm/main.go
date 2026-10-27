// fakekvm runs a fake NanoKVM (firmware 2.5 API) with a simulated computer behind it, for trying the
// app or an assistant against a hub without hardware:
//
//	go run ./tools/fakekvm -on
//
// Then link it from the app (or POST /v2/kvms on a development helper started with
// POWERDECK_DATA set; loopback KVMs are allowed). Type commands to change the computer:
// on, off, hang, state, quit.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/projectzero02/powerdeck/helper/internal/nanokvm/fakekvm"
)

func main() {
	var o fakekvm.Options
	flag.StringVar(&o.User, "user", "admin", "user name")
	flag.StringVar(&o.Password, "password", "admin", "password")
	flag.StringVar(&o.Style, "style", fakekvm.Style25, "API style: "+fakekvm.Style25+", "+fakekvm.Style24+" or "+fakekvm.StylePro)
	flag.BoolVar(&o.On, "on", false, "the computer starts on")
	flag.BoolVar(&o.HTTPS, "https", false, "HTTPS mode: plain HTTP redirects to a self-signed port")
	flag.BoolVar(&o.NoLED, "no-led", false, "power light not wired: always reads off")
	flag.BoolVar(&o.NoPowerSwitch, "no-power-switch", false, "power button not wired")
	flag.BoolVar(&o.NoResetSwitch, "no-reset-switch", false, "reset button not wired")
	flag.BoolVar(&o.Forbidden, "forbidden", false, "the account lacks the power permission")
	flag.Float64Var(&o.TimeScale, "time-scale", 0.2, "speed of boots and shutdowns (1 = real: 30 s boot)")
	flag.Parse()

	f := fakekvm.Start(o)
	defer f.Close()
	fmt.Printf("fake NanoKVM at %s (user %s)\n", f.URL, o.User)
	if o.HTTPS {
		fmt.Printf("HTTPS at %s, certificate pin %s\n", f.TLSURL, f.TLSPin)
	}
	fmt.Println("commands: on, off, hang, state, quit")
	in := bufio.NewScanner(os.Stdin)
	for fmt.Print("> "); in.Scan(); fmt.Print("> ") {
		switch strings.TrimSpace(in.Text()) {
		case "on":
			f.SetOn(true)
		case "off":
			f.SetOn(false)
		case "hang":
			f.Hang()
		case "state", "":
		case "quit", "exit":
			return
		default:
			fmt.Println("commands: on, off, hang, state, quit")
			continue
		}
		fmt.Printf("power %v, OS up %v, reboots %d, presses %v, wakes %v\n", f.On(), f.Up(), f.Reboots(), f.Presses(), f.Wakes())
	}
}
