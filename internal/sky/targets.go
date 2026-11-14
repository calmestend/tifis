package sky

import (
	"hash/fnv"
	"math/rand/v2"
)

// Target is one of the test objects of the challenge. Positions are J2000 at
// epoch J2000 and proper motions come from Simbad (Gaia DR3, CatWISE for WISE 0855).
type Target struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Aliases []string `json:"aliases"`
	Band    int      `json:"band"` // suggested channel, 0-based
	Mode    string   `json:"mode"` // suggested view mode
	Source  Source   `json:"-"`
}

var Targets = []Target{
	{
		ID: "barnard", Name: "Barnard's Star", Band: 1, Mode: "diff",
		Aliases: []string{"barnard", "estrella de barnard", "gj 699", "barnard's star"},
		Source: Source{
			Name: "Barnard's Star", RA: 269.45207696, Dec: 4.69336497, PMRA: -801.551, PMDec: 10362.394,
			RefMJD: MJDJ2000, Kind: SEDStar, Temp: 3200, Flux: fluxFromMag(4.52), RefLambda: 2.2, Catalog: true,
		},
	},
	{
		ID: "eps-indi-b", Name: "Epsilon Indi B", Band: 2, Mode: "diff",
		Aliases: []string{"eps ind b", "epsilon indi b", "eps indi b", "ε indi b", "epsilon indi ba"},
		Source: Source{
			Name: "Epsilon Indi Ba+Bb", RA: 331.04414659, Dec: -56.78284420, PMRA: 3981.977, PMDec: -2466.832,
			RefMJD: MJDJ2000, Kind: SEDBrownDwarf, Temp: 1100, Flux: fluxFromMag(11.21), RefLambda: 2.2, Catalog: true,
		},
	},
	{
		ID: "wise0855", Name: "WISE 0855-0714", Band: 5, Mode: "diff",
		Aliases: []string{"wise 0855", "wise 0855-0714", "wise j0855", "wisea j085510.74-071442.5", "wise j085510.83-071442.5"},
		Source: Source{
			Name: "WISE 0855-0714", RA: 133.81890825, Dec: -7.24704800, PMRA: -8123.7, PMDec: 673.2,
			RefMJD: MJDJ2000, Kind: SEDYDwarf, Temp: 285, Flux: fluxFromMag(13.9), RefLambda: 4.6, Catalog: true,
		},
	},
	{
		ID: "rw-aur", Name: "RW Aurigae", Band: 3, Mode: "epoch",
		Aliases: []string{"rw aur", "rw aurigae"},
		Source: Source{
			Name: "RW Aur", RA: 76.95652603, Dec: 30.40143817, PMRA: 7.23, PMDec: -20.33,
			RefMJD: MJDJ2000, Kind: SEDYoung, Temp: 4500, Flux: fluxFromMag(7.02), RefLambda: 2.2,
			Amp: 0.7, Catalog: true,
		},
	},
	{
		ID: "r-dra", Name: "R Draconis", Band: 2, Mode: "epoch",
		Aliases: []string{"r dra", "r draconis"},
		// K is 2.2 in reality; the simulation dims it 2 mag so the core only
		// saturates near maximum and the pulsation stays visible.
		Source: Source{
			Name: "R Dra", RA: 248.16761083, Dec: 66.75497942, PMRA: -20.643, PMDec: 6.149,
			RefMJD: MJDJ2000, Kind: SEDMira, Temp: 2600, Flux: fluxFromMag(4.2), RefLambda: 2.2,
			Period: 245.6, Amp: 0.9, Phase: 60950, Catalog: true,
		},
	},
}

func init() {
	for i := range Targets {
		Targets[i].Source.seed = hashString(Targets[i].ID)
	}
}

func hashString(s string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(s))
	return h.Sum64()
}

func newRand(seed uint64) *rand.Rand {
	return rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
}
