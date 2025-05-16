package macaroons

import (
	"encoding/base64"
	"encoding/json"

	"github.com/cloudcarver/anchor/pkg/utils"
	"github.com/pkg/errors"
)

var (
	ErrCaveatCheckFailed = errors.New("caveat check failed")
)

type CaveatConstructor func() Caveat

type CaveatParser struct {
	caveats map[string]CaveatConstructor
}

func NewCaveatParser() CaveatParserInterface {
	return &CaveatParser{
		caveats: make(map[string]CaveatConstructor),
	}
}

func (c *CaveatParser) Register(typ string, v CaveatConstructor) error {
	if _, ok := c.caveats[typ]; ok {
		return errors.Errorf("caveat type %s already registered", typ)
	}
	c.caveats[typ] = v
	return nil
}

func (c *CaveatParser) Parse(s string) (Caveat, error) {
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to decode base64 encoded caveat, raw: %s", s)
	}

	typ, err := utils.RetrieveFromJSON[string](string(decoded), "type")
	if err != nil {
		return nil, err
	}

	constructor, ok := c.caveats[*typ]
	if !ok {
		return nil, errors.Errorf("unknown caveat type: %s", *typ)
	}

	ret := constructor()

	err = DecodeCaveat(s, ret)
	if err != nil {
		return nil, err
	}

	return ret, nil
}

// EncodeCaveat encodes a caveat to base64 string
func EncodeCaveat(v interface{}) (string, error) {
	json, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(json), nil
}

// DecodeCaveat decodes a base64 string to a caveat
func DecodeCaveat(s string, v interface{}) error {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return err
	}

	err = json.Unmarshal(raw, v)
	if err != nil {
		return err
	}

	return nil
}
