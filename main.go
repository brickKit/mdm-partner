// Command mdm-partner runs the partner component alone (serve, migrate); the shells compile in partner.Definition().
package main

import (
	"github.com/brickKit/be-sdk-go/v3/launcher"
	"github.com/brickKit/mdm-partner/v3/partner"
)

func main() { launcher.Main(partner.Definition()) }
