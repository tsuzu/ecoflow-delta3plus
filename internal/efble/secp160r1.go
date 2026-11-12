package efble

import (
	"crypto/rand"
	"errors"
	"math/big"
)

// secp160r1 arithmetic with math/big. Go's crypto/elliptic has no
// P-160, but the EcoFlow handshake uses SECP160r1 (via the Python
// ecdsa library in the reference implementation). Parameters are the
// standard SEC 2 values, cross-checked against the ecdsa package.
//
// Only scalar multiplication of the generator (key generation) and
// of the peer point (ECDH) are needed; this is handshake-rate work,
// so constant-time operation is not a concern for our own ephemeral
// key beyond what big.Int provides.

var (
	secp160r1P, _  = new(big.Int).SetString("FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF7FFFFFFF", 16)
	secp160r1B, _  = new(big.Int).SetString("1C97BEFC54BD7A8B65ACF89F81D4D4ADC565FA45", 16)
	secp160r1Gx, _ = new(big.Int).SetString("4A96B5688EF573284664698968C38BB913CBFC82", 16)
	secp160r1Gy, _ = new(big.Int).SetString("23A628553168947D59DCC912042351377AC5FB32", 16)
	secp160r1N, _  = new(big.Int).SetString("0100000000000000000001F4C8F927AED3CA752257", 16)
)

const secp160r1Size = 20 // coordinate size in bytes

type ecPoint struct {
	x, y *big.Int // nil, nil = point at infinity
}

func secp160r1A() *big.Int {
	// a = p - 3
	return new(big.Int).Sub(secp160r1P, big.NewInt(3))
}

func pointAdd(p1, p2 ecPoint) ecPoint {
	if p1.x == nil {
		return p2
	}
	if p2.x == nil {
		return p1
	}
	mod := secp160r1P
	var lambda *big.Int
	if p1.x.Cmp(p2.x) == 0 {
		if new(big.Int).Add(p1.y, p2.y).Cmp(mod) == 0 || p1.y.Sign() == 0 {
			return ecPoint{}
		}
		// lambda = (3*x1^2 + a) / (2*y1)
		num := new(big.Int).Mul(p1.x, p1.x)
		num.Mul(num, big.NewInt(3))
		num.Add(num, secp160r1A())
		num.Mod(num, mod)
		den := new(big.Int).Lsh(p1.y, 1)
		den.Mod(den, mod)
		den.ModInverse(den, mod)
		lambda = num.Mul(num, den)
		lambda.Mod(lambda, mod)
	} else {
		// lambda = (y2 - y1) / (x2 - x1)
		num := new(big.Int).Sub(p2.y, p1.y)
		num.Mod(num, mod)
		den := new(big.Int).Sub(p2.x, p1.x)
		den.Mod(den, mod)
		den.ModInverse(den, mod)
		lambda = num.Mul(num, den)
		lambda.Mod(lambda, mod)
	}
	x3 := new(big.Int).Mul(lambda, lambda)
	x3.Sub(x3, p1.x)
	x3.Sub(x3, p2.x)
	x3.Mod(x3, mod)
	y3 := new(big.Int).Sub(p1.x, x3)
	y3.Mul(lambda, y3)
	y3.Sub(y3, p1.y)
	y3.Mod(y3, mod)
	return ecPoint{x3, y3}
}

func scalarMult(k *big.Int, p ecPoint) ecPoint {
	result := ecPoint{}
	addend := p
	for i := 0; i < k.BitLen(); i++ {
		if k.Bit(i) == 1 {
			result = pointAdd(result, addend)
		}
		addend = pointAdd(addend, addend)
	}
	return result
}

type ecdhKeyPair struct {
	priv *big.Int
	pub  ecPoint
}

// generateKeyPair mirrors ecdsa.SigningKey.generate(SECP160r1).
func generateKeyPair() (*ecdhKeyPair, error) {
	max := new(big.Int).Sub(secp160r1N, big.NewInt(1))
	d, err := rand.Int(rand.Reader, max)
	if err != nil {
		return nil, err
	}
	d.Add(d, big.NewInt(1))
	pub := scalarMult(d, ecPoint{secp160r1Gx, secp160r1Gy})
	return &ecdhKeyPair{priv: d, pub: pub}, nil
}

// publicBytes returns X||Y, each zero-padded to 20 bytes, matching
// ecdsa's VerifyingKey.to_string() for this curve.
func (kp *ecdhKeyPair) publicBytes() []byte {
	out := make([]byte, 0, 2*secp160r1Size)
	out = append(out, padTo(kp.pub.x, secp160r1Size)...)
	out = append(out, padTo(kp.pub.y, secp160r1Size)...)
	return out
}

func parsePublicBytes(b []byte) (ecPoint, error) {
	if len(b) != 2*secp160r1Size {
		return ecPoint{}, errors.New("bad public key length")
	}
	x := new(big.Int).SetBytes(b[:secp160r1Size])
	y := new(big.Int).SetBytes(b[secp160r1Size:])
	// Verify the point is on the curve: y^2 == x^3 + a*x + b (mod p)
	lhs := new(big.Int).Mul(y, y)
	lhs.Mod(lhs, secp160r1P)
	rhs := new(big.Int).Mul(x, x)
	rhs.Mul(rhs, x)
	tmp := new(big.Int).Mul(secp160r1A(), x)
	rhs.Add(rhs, tmp)
	rhs.Add(rhs, secp160r1B)
	rhs.Mod(rhs, secp160r1P)
	if lhs.Cmp(rhs) != 0 {
		return ecPoint{}, errors.New("point not on secp160r1")
	}
	return ecPoint{x, y}, nil
}

// sharedSecret returns the ECDH shared secret as the padded X
// coordinate, matching ecdsa's ECDH.generate_sharedsecret_bytes().
func (kp *ecdhKeyPair) sharedSecret(peer ecPoint) ([]byte, error) {
	shared := scalarMult(kp.priv, peer)
	if shared.x == nil {
		return nil, errors.New("shared point at infinity")
	}
	return padTo(shared.x, secp160r1Size), nil
}

func padTo(n *big.Int, size int) []byte {
	b := n.Bytes()
	if len(b) >= size {
		return b[len(b)-size:]
	}
	out := make([]byte, size)
	copy(out[size-len(b):], b)
	return out
}
