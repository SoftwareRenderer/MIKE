package types

import (
	"crypto/rand"
	"math/big"
)

var taskWords = []string{
	// landscape
	"ocean", "river", "mountain", "forest", "desert", "island", "valley", "meadow",
	"garden", "stream", "canyon", "glacier", "volcano", "waterfall", "tide", "reef",
	"lagoon", "dune", "marsh", "ridge", "peak", "cliff", "cave", "grotto",
	"fjord", "prairie", "tundra", "jungle", "grove", "glade", "brook", "creek",
	"delta", "oasis", "taiga", "heath", "dale", "hollow",
	// weather & sky
	"sunrise", "sunset", "thunder", "lightning", "rainbow", "aurora", "breeze", "storm",
	"frost", "snow", "twilight", "solstice", "equinox", "cyclone", "monsoon", "zephyr",
	"gust", "tempest", "squall", "drizzle", "hail", "sleet", "blizzard", "mist",
	"fog", "haze", "dew", "rime", "smoke", "vapor", "gloom",
	// land animals
	"eagle", "wolf", "bear", "fox", "hawk", "owl", "deer", "lion",
	"tiger", "panda", "falcon", "raven", "heron", "lynx", "otter", "badger",
	"wren", "ibis", "stork", "crane", "swan", "cobra", "viper", "python",
	// sea creatures
	"shark", "dolphin", "whale", "jelly", "manta", "turtle", "salmon", "trout",
	// colors & gems
	"crimson", "azure", "emerald", "amber", "violet", "coral", "sapphire", "ruby",
	"gold", "silver", "bronze", "copper", "ivory", "pearl", "onyx", "jade",
	"scarlet", "blush", "mauve", "umber", "ochre", "teal",
	// space & cosmic
	"nova", "comet", "meteor", "galaxy", "nebula", "planet", "star", "cosmos",
	"orbit", "pulse", "zenith", "eclipse", "lunar", "solar", "stellar",
	"quasar", "void", "rift", "flux", "arc", "beam", "flare", "shard",
	// materials & minerals
	"flame", "stone", "iron", "steel", "crystal", "diamond", "obsidian", "marble",
	"granite", "basalt", "quartz", "flint", "coal", "ash", "ember", "spark",
	"slate",
	// abstract & virtues
	"dream", "vision", "spirit", "soul", "mind", "heart", "voice", "echo",
	"shadow", "light", "truth", "wisdom", "power", "grace", "honor", "valor",
	"brave", "keen", "bold", "firm", "fleet", "bright", "clear", "calm",
	"noble", "proud", "lucky", "witty", "sharp", "stout", "vivid",
	// architecture
	"gate", "arch", "dome", "spire", "tower", "fort", "keep", "hall",
	"vault", "crypt", "sanctum", "haven", "shrine", "forge", "anvil",
	"blade", "helm", "crest", "shield", "torch", "lantern",
	"prism", "cipher", "token", "sigil", "seal", "ward", "oath", "rune",
	// stories
	"fable", "lore", "myth", "saga", "verse", "chant", "hymn", "ballad",
	"quest", "trail", "path",
	// flora
	"bloom", "petal", "thorn", "bark", "leaf", "vine", "root", "seed",
	"flora", "moss",
	// movement & dwelling
	"soar", "roam", "wander", "stray", "roost", "nest", "den", "lair",
	"glide", "surge",
	// misc
	"plume", "drift",
}

// secureIndex returns a uniformly distributed index in [0, n) from the
// cryptographic random source. Task and thread IDs surface in URLs, git branch
// names and worktree paths, so they must not be guessable — math/rand seeded
// from the clock was. A failure here means the system's random source is gone
// (token generation would fail the same way), so it panics rather than handing
// out a weak ID.
func secureIndex(n int) int {
	if n <= 0 {
		return 0
	}
	i, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		panic("types: secure random source unavailable: " + err.Error())
	}
	return int(i.Int64())
}

// pickTaskWord draws one dictionary word.
func pickTaskWord() string {
	return taskWords[secureIndex(len(taskWords))]
}

// GenerateTaskID returns a human-readable task ID (e.g., "frost-amber-quartz").
func GenerateTaskID() string {
	return pickTaskWord() + "-" + pickTaskWord() + "-" + pickTaskWord()
}

// GenerateThreadID returns a human-readable thread ID (e.g., "voice-ash").
func GenerateThreadID() string {
	return pickTaskWord() + "-" + pickTaskWord()
}
