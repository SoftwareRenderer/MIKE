package types

import (
	"math/rand"
	"time"
)

var taskWords = []string{
	"ocean", "river", "mountain", "forest", "desert", "island", "valley", "meadow",
	"garden", "stream", "canyon", "glacier", "volcano", "waterfall", "sunrise", "sunset",
	"thunder", "lightning", "rainbow", "aurora", "breeze", "storm", "frost", "snow",
	"eagle", "wolf", "bear", "fox", "hawk", "owl", "deer", "lion",
	"tiger", "shark", "dolphin", "whale", "panda", "falcon", "raven", "heron",
	"crimson", "azure", "emerald", "amber", "violet", "coral", "sapphire", "ruby",
	"gold", "silver", "bronze", "copper", "ivory", "pearl", "onyx", "jade",
	"nova", "comet", "meteor", "galaxy", "nebula", "planet", "star", "cosmos",
	"orbit", "pulse", "zenith", "eclipse", "lunar", "solar", "stellar",
	"flame", "stone", "iron", "steel", "crystal", "diamond", "obsidian", "marble",
	"granite", "basalt", "quartz", "flint", "coal", "ash", "ember", "spark",
	"dream", "vision", "spirit", "soul", "mind", "heart", "voice", "echo",
	"shadow", "light", "truth", "wisdom", "power", "grace", "honor", "valor",
	"tide", "reef", "lagoon", "dune", "marsh", "ridge", "peak", "cliff",
	"cave", "grotto", "fjord", "prairie", "tundra", "jungle", "grove", "glade",
	"brook", "creek", "delta", "oasis", "taiga", "heath", "dale", "hollow",
	"twilight", "solstice", "equinox", "cyclone", "monsoon", "zephyr", "gust", "tempest",
	"squall", "drizzle", "hail", "sleet", "blizzard", "mist", "fog", "haze",
	"dew", "rime", "smoke", "vapor", "gloom", "swell", "surge", "glide",
	"lynx", "otter", "badger", "wren", "ibis", "stork", "crane", "swan",
	"cobra", "viper", "python", "jelly", "manta", "turtle", "salmon", "trout",
	"scarlet", "blush", "drift", "slate", "mauve", "umber", "ochre", "teal",
	"quasar", "void", "rift", "flux", "arc", "beam", "flare", "shard",
	"forge", "anvil", "blade", "helm", "crest", "shield", "torch", "lantern",
	"prism", "cipher", "token", "sigil", "seal", "ward", "oath", "rune",
	"fable", "lore", "myth", "saga", "verse", "chant", "hymn", "ballad",
	"quest", "trail", "path", "gate", "arch", "dome", "spire", "tower",
	"fort", "keep", "hall", "vault", "crypt", "sanctum", "haven", "shrine",
	"bloom", "petal", "thorn", "bark", "leaf", "vine", "root", "seed",
	"soar", "roam", "wander", "stray", "roost", "nest", "den", "lair",
	"brave", "keen", "bold", "firm", "fleet", "bright", "clear",
	"noble", "proud", "lucky", "witty", "sharp", "stout", "vivid", "plume", "flora", "moss",
}

// GenerateTaskID returns a human-readable task ID (e.g., "frost-amber-quartz").
func GenerateTaskID() string {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	w1 := taskWords[r.Intn(len(taskWords))]
	w2 := taskWords[r.Intn(len(taskWords))]
	w3 := taskWords[r.Intn(len(taskWords))]
	return w1 + "-" + w2 + "-" + w3
}

// GenerateThreadID returns a human-readable thread ID (e.g., "voice-ash").
func GenerateThreadID() string {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	w1 := taskWords[r.Intn(len(taskWords))]
	w2 := taskWords[r.Intn(len(taskWords))]
	return w1 + "-" + w2
}
