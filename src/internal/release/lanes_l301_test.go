package release

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"
)

func TestLanesL301CanonicalizationGoldenVectors(t *testing.T) {
	input := []byte(`{"numbers":[333333333.33333329,1E30,4.50,2e-3,0.000000000000000000000000001],"string":"€$\u000f\nA'B\"\\\\\"/"}`)
	want := []byte(`{"numbers":[333333333.3333333,1e+30,4.5,0.002,1e-27],"string":"€$\u000f\nA'B\"\\\\\"/"}`)
	got, err := CanonicalizeJSON(input)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("JCS sample = %s, want %s", got, want)
	}

	// RFC 8785 orders property names by UTF-16 code units, so U+10000 sorts
	// before U+E000 even though its Unicode scalar value is larger.
	got, err = CanonicalizeJSON([]byte(`{"\ue000":1,"\ud800\udc00":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"𐀀":2,"":1}`; string(got) != want {
		t.Fatalf("UTF-16 key order = %s, want %s", got, want)
	}
	got, err = CanonicalizeJSON([]byte(`[1e-6,1e-7,1e20,1e21,-1.25,-1e-6,-1e-7]`))
	if err != nil {
		t.Fatal(err)
	}
	if want := `[0.000001,1e-7,100000000000000000000,1e+21,-1.25,-0.000001,-1e-7]`; string(got) != want {
		t.Fatalf("ECMAScript number thresholds = %s, want %s", got, want)
	}
	for _, vector := range []struct{ bits, want string }{
		{"0000000000000001", "5e-324"},
		{"8000000000000001", "-5e-324"},
		{"7fefffffffffffff", "1.7976931348623157e+308"},
		{"ffefffffffffffff", "-1.7976931348623157e+308"},
		{"4340000000000000", "9007199254740992"},
		{"c340000000000000", "-9007199254740992"},
		{"4430000000000000", "295147905179352830000"},
		{"44b52d02c7e14af5", "9.999999999999997e+22"},
		{"44b52d02c7e14af6", "1e+23"},
		{"44b52d02c7e14af7", "1.0000000000000001e+23"},
		{"444b1ae4d6e2ef4e", "999999999999999700000"},
		{"444b1ae4d6e2ef4f", "999999999999999900000"},
		{"444b1ae4d6e2ef50", "1e+21"},
		{"3eb0c6f7a0b5ed8c", "9.999999999999997e-7"},
		{"3eb0c6f7a0b5ed8d", "0.000001"},
		{"41b3de4355555553", "333333333.3333332"},
		{"41b3de4355555554", "333333333.33333325"},
		{"41b3de4355555555", "333333333.3333333"},
		{"41b3de4355555556", "333333333.3333334"},
		{"41b3de4355555557", "333333333.33333343"},
		{"becbf647612f3696", "-0.0000033333333333333333"},
		{"43143ff3c1cb0959", "1424953923781206.2"},
	} {
		bits, err := strconv.ParseUint(vector.bits, 16, 64)
		if err != nil {
			t.Fatal(err)
		}
		if got := ecmaNumber(math.Float64frombits(bits)); got != vector.want {
			t.Errorf("ECMAScript number bits %s = %s, want %s", vector.bits, got, vector.want)
		}
	}
	for _, invalid := range []string{`{"v":-0}`, `{"v":-0.0}`, `{"v":1e9999}`, `{"x":1,"x":2}`, `{"v":"\ud800"}`, `{"v":"\udc00"}`} {
		if _, err := CanonicalizeJSON([]byte(invalid)); !errors.Is(err, ErrInvalidCanonicalJSON) {
			t.Errorf("CanonicalizeJSON(%s) error = %v, want ErrInvalidCanonicalJSON", invalid, err)
		}
	}
}

func TestLanesL301EnvelopeGoldenVectors(t *testing.T) {
	authorityPrivate := testSigningKey(0)
	hostPrivate := testSigningKey(32)
	keys := map[string]TrustedKey{
		"authority-test":  {KeyID: "authority-test", Role: SignerRoleReleaseAuthority, PublicKey: authorityPrivate.Public().(ed25519.PublicKey)},
		"host-linux-test": {KeyID: "host-linux-test", Role: SignerRoleHostAttestation, HostID: "linux-host", PublicKey: hostPrivate.Public().(ed25519.PublicKey)},
	}
	vectors := []struct {
		typeName, role, keyID, hostID, payload, recordID, signature string
	}{
		{"host_build_attestation", "host", "host-linux-test", "linux-host", `{"host_id":"linux-host","vector":1}`, "5da7250a0e53813309f7040f3e82ffb4f599336546232624c9abbdf2e52369db", "DDyCp+LhWH0r4VMPnpUj88pgzpX6ZAmSY/vvMrPCK1iDi9vxhkczTgJEhFhCIn3YjzsgdcP4Bmq/1hIDPk+tAw=="},
		{"candidate_manifest", "authority", "authority-test", "", `{"scope":"authority","vector":1}`, "182ca334fb6ca57d7ea5bbce6ecef8e8540d366cc69109b91a22563f051eea6f", "y0jmYkvSMCkfZCgUzqv6nQVR3YFNvkKqXJ/9dG7LK4uX5dS1HyPsq2c6E7iNAXEJTDvt0txl8DZofThQO+24BA=="},
		{"promotion_authorization", "authority", "authority-test", "", `{"scope":"authority","vector":1}`, "7448256d2a2c9507f7352e43fe48ddb9958c5aeecbf2c073508f96a18d56805d", "SVnDsidAfP322l7fv8w4Ysvd+cLdlHx7Q9czweKby2aDCC8e/Evg6+SsaiIbLVTyw9S6Bg/4tm1Abz7sKYmpBg=="},
		{"promotion_attestation", "host", "host-linux-test", "linux-host", `{"host_id":"linux-host","vector":1}`, "f0f7816c23806ef3bfb49dcaafbf21245a04320ab8fb4179a4eff7a33be7d943", "8Hw6KU5pAQIgH3MP72lXxoDeVBJYFhgrI+jSas7s5Ziuzknf+7TRoz1qvJCF9aIj8Nsm4WZX/qBFHV6D22XkBQ=="},
		{"lane_acceptance", "authority", "authority-test", "", `{"scope":"authority","vector":1}`, "902cd95b2694bcaa1414965465586437db4a0350c3a836f1e99934bf0c0fe08b", "ViVfdI2UKsCjkvOqRrpXcZaHcNZtIyxILZekKWFF1HTNax0yuJYQqg1hJo+7koggYW7LPTtQk+xhAYyln9dEAw=="},
		{"ingress_admission_directive", "authority", "authority-test", "", `{"scope":"authority","vector":1}`, "11f9b7dec7ac6b46011276cf96f9d8e7f7f660aea315a27deca70be72482ee23", "C2micYKvouCWbFh/JKWjGHb4G++BgYQBighPo6mF6fJx2TDU0F58Oc1bwjydcwM0F/te5YdkqYU71E1dLpcMBQ=="},
		{"ingress_admission_attestation", "host", "host-linux-test", "linux-host", `{"host_id":"linux-host","vector":1}`, "ebaa5241570b30acb782769d622fd4882a068a2c6a3be7fb35e4bcb10b439384", "A3ZNJDnYB9XZMkeLl129rKeEK+Opud60H03mn8V4r806PCC777G28ZohB6UsKt2U/pZV+ZIfsote+P4Jyo9fDQ=="},
		{"capacity_measurement_attestation", "host", "host-linux-test", "linux-host", `{"host_id":"linux-host","vector":1}`, "db46c8ab6e532d2c744e21972d987695d97699beea6c5ee4335266fdeb9bc9cf", "PE5/FQZXaqGB2ksopNO/PiPnReeWUNxl0ED3rulQIWeHOMUtiZwHgJxwOJe4uEgziGv5W6RP1CZE4CfUIGCnBw=="},
		{"capacity_policy", "authority", "authority-test", "", `{"scope":"authority","vector":1}`, "6484ef0f1d96775f9d08be9ce8e037aaaa990fde54125b5be6b770739f582fe0", "ZHQbqXh/YRW/LFG+TK3j6ws1uD8rgXI8KfJ/SBCrDByLX0SYaWMYzRJ+Bxrq1o8rcIAuoNGFIPriDbEXBydMCQ=="},
		{"pki_certificate_request", "host", "host-linux-test", "linux-host", `{"host_id":"linux-host","vector":1}`, "194c13674775c25e4b45d86b67503e8e66ecafc5f7f62bd5b6911376f40cf697", "TNUrKz7cpgCzKKlDsVCjJyWgPZOUGmk1DPeqeurqQ5bHNeG+Ai9S7WPKfrB3UuIemuT7cl9WmWRXZs5O3JyfCw=="},
		{"host_management_manifest", "authority", "authority-test", "", `{"scope":"authority","vector":1}`, "c8ace815350bdcd33e097d9d70b08650913c1e4931ad5765f6438860e543b4eb", "6GyCRZ1JdTqW+0zpSXGdNK4dcQg9qp/+dPxZeEZra1SDUmv2Otinn6fMiLZlkhjNEy/cLPyzzJPU5NcTGAKUDQ=="},
		{"host_management_authorization", "authority", "authority-test", "", `{"scope":"authority","vector":1}`, "513ebf09c5089a932f912678b6fd3c0610bf23a7914b62b8d2e08206b27d6a8f", "hQN1DZf5TYAjdzAl6keNQX85fWOY2VglNh/J/XU+6vcqjb9t0oF0pNOwpcFisWHKc1bN0ZRo8BMcT/oO4NsAAg=="},
		{"host_management_attestation", "host", "host-linux-test", "linux-host", `{"host_id":"linux-host","vector":1}`, "b8143a7407796628f821ff196de2f2678713934d05318061f2bb5679d2147cdb", "T/ar9taLjpEMpcwZpsPy8ui6HKDQH6w6LGUocW7IKXm1NQrpg/LUGGKRRvU9C/ipRjteTeXb4J4blTv6fwbFDg=="},
		{"host_attestation_key_set", "authority", "authority-test", "", `{"scope":"authority","vector":1}`, "8ccdfdf6a8eb695e8081f6b47fb1dcf09d56e74b47d61a767e71c86c0bae1fee", "mX3xrweeBS5AJUUKkcfx573bGfD6oX0KHvnskiZJUVIZ9IqQiGUlJh+KaffBdPqJFSSIpRym3COhaPomDmF0Cw=="},
		{"release_revocation_ledger", "authority", "authority-test", "", `{"scope":"authority","vector":1}`, "d45b672e75fa4aac821e033e9d108f24cc9a5f8a5cce7b75627ff8e1eeefe206", "WzBwU/9nGdqsSh5HnAQKQSM/I7d5+BsHkQOeCbpurMcKXOKqh53IaVBNnYVVxQAVjLIKbRmEDCDT5rjbqiV7AA=="},
		{"authority_key_set", "previous", "authority-test", "", `{"previous_key_id":"authority-test","vector":1}`, "2e45d12d21c51b59054765ec01bd2f488fd9765cd67d09650f35d89539d6707d", "WltVk3BMniHzVogh4wZEuxFDdz1B3t1mqi3VMVRYepOjzI9kmmHNm5mW8zgO5ZKKayMGrxvtJvnUd0dfYnLGDw=="},
	}
	for _, vector := range vectors {
		t.Run(vector.typeName, func(t *testing.T) {
			privateKey, role, hostID := authorityPrivate, SignerRoleReleaseAuthority, ""
			if vector.role == "host" {
				privateKey, role, hostID = hostPrivate, SignerRoleHostAttestation, vector.hostID
			} else if vector.role == "previous" {
				role = SignerRolePreviousAuthorityKey
			}
			got, err := SignEnvelope(vector.typeName, []byte(vector.payload), SigningIdentity{
				KeyID: vector.keyID, Role: role, HostID: hostID, PrivateKey: privateKey,
			})
			if err != nil {
				t.Fatal(err)
			}
			want := fmt.Sprintf(`{"payload":%s,"record_id":%q,"record_type":%q,"release_record_version":1,"signature":%q,"signature_algorithm":"Ed25519","signer_key_id":%q}`,
				vector.payload, vector.recordID, vector.typeName, vector.signature, vector.keyID)
			if string(got) != want {
				t.Fatalf("envelope = %s, want %s", got, want)
			}
			verified, err := VerifyEnvelope(got, keys)
			if err != nil {
				t.Fatalf("VerifyEnvelope rejected golden vector: %v", err)
			}
			if verified.RecordType != vector.typeName || verified.RecordID != vector.recordID || !bytes.Equal(verified.Payload, []byte(vector.payload)) {
				t.Fatalf("verified envelope = %+v", verified)
			}
		})
	}
}

func TestLanesL301RejectsUnknownRecordTypesAndRoles(t *testing.T) {
	for _, recordType := range []string{"", "unknown", "Authority_key_set", "authority_key_set/v2"} {
		if _, ok := SignerRoleForRecordType(recordType); ok {
			t.Errorf("SignerRoleForRecordType(%q) unexpectedly succeeded", recordType)
		}
		if _, err := SignEnvelope(recordType, []byte(`{"x":1}`), SigningIdentity{}); !errors.Is(err, ErrUnknownRecordType) {
			t.Errorf("SignEnvelope(%q) error = %v, want ErrUnknownRecordType", recordType, err)
		}
	}
	if _, err := SignEnvelope("candidate_manifest", []byte(`{"scope":"authority","vector":1}`), SigningIdentity{
		KeyID: "host-linux-test", Role: SignerRoleHostAttestation, HostID: "linux-host", PrivateKey: testSigningKey(32),
	}); !errors.Is(err, ErrSignerRoleMismatch) {
		t.Fatalf("wrong role signing candidate_manifest = %v", err)
	}
	if _, err := SignEnvelope("candidate_manifest", []byte(`{"scope":"authority","vector":1}`), SigningIdentity{
		KeyID: "authority-test", Role: SignerRole("unknown"), PrivateKey: testSigningKey(0),
	}); !errors.Is(err, ErrSignerRoleMismatch) {
		t.Fatalf("unknown signer role error = %v, want ErrSignerRoleMismatch", err)
	}
}

func TestLanesL301RejectsNoncanonicalAndOpenEnvelopes(t *testing.T) {
	key := testSigningKey(0)
	valid, err := SignEnvelope("candidate_manifest", []byte(`{"scope":"authority","vector":1}`), SigningIdentity{
		KeyID: "authority-test", Role: SignerRoleReleaseAuthority, PrivateKey: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	trusted := map[string]TrustedKey{"authority-test": {KeyID: "authority-test", Role: SignerRoleReleaseAuthority, PublicKey: key.Public().(ed25519.PublicKey)}}
	if _, err := VerifyEnvelope(valid, nil); !errors.Is(err, ErrUntrustedSigner) {
		t.Fatalf("unknown signer error = %v, want ErrUntrustedSigner", err)
	}
	if _, err := VerifyEnvelope(append([]byte(" "), valid...), trusted); !errors.Is(err, ErrNonCanonicalRecord) {
		t.Fatalf("whitespace envelope error = %v, want ErrNonCanonicalRecord", err)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(valid, &fields); err != nil {
		t.Fatal(err)
	}
	fields["unexpected"] = json.RawMessage(`true`)
	unknownFieldRaw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	unknownField, err := CanonicalizeJSON(unknownFieldRaw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyEnvelope(unknownField, trusted); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("unknown envelope field error = %v, want ErrInvalidEnvelope", err)
	}

	duplicate := bytes.Replace(valid, []byte(`"record_type":`), []byte(`"record_type":"candidate_manifest","record_type":`), 1)
	if _, err := VerifyEnvelope(duplicate, trusted); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("duplicate envelope field error = %v, want ErrInvalidEnvelope", err)
	}

	unknownType := bytes.Replace(valid, []byte(`"record_type":"candidate_manifest"`), []byte(`"record_type":"future_record"`), 1)
	unknownType, err = CanonicalizeJSON(unknownType)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyEnvelope(unknownType, trusted); !errors.Is(err, ErrUnknownRecordType) {
		t.Fatalf("unknown record type error = %v, want ErrUnknownRecordType", err)
	}
}

func TestLanesL301EnforcesTrustedRoleAndCanonicalHostBinding(t *testing.T) {
	authorityPrivate := testSigningKey(0)
	hostPrivate := testSigningKey(32)
	valid, err := SignEnvelope("candidate_manifest", []byte(`{"scope":"authority","vector":1}`), SigningIdentity{
		KeyID: "authority-test", Role: SignerRoleReleaseAuthority, PrivateKey: authorityPrivate,
	})
	if err != nil {
		t.Fatal(err)
	}
	wrongRole := map[string]TrustedKey{"authority-test": {
		KeyID: "authority-test", Role: SignerRoleHostAttestation, HostID: "linux-host", PublicKey: authorityPrivate.Public().(ed25519.PublicKey),
	}}
	if _, err := VerifyEnvelope(valid, wrongRole); !errors.Is(err, ErrSignerRoleMismatch) {
		t.Fatalf("candidate signed by host-role key error = %v", err)
	}
	if _, err := SignEnvelope("host_build_attestation", []byte(`{"host_id":"sandbox-host","vector":1}`), SigningIdentity{
		KeyID: "host-linux-test", Role: SignerRoleHostAttestation, HostID: "linux-host", PrivateKey: hostPrivate,
	}); !errors.Is(err, ErrSignerRoleMismatch) {
		t.Fatalf("foreign-host payload signing error = %v", err)
	}
	hostRecord, err := SignEnvelope("host_build_attestation", []byte(`{"host_id":"linux-host","vector":1}`), SigningIdentity{
		KeyID: "host-linux-test", Role: SignerRoleHostAttestation, HostID: "linux-host", PrivateKey: hostPrivate,
	})
	if err != nil {
		t.Fatal(err)
	}
	foreignHostKey := map[string]TrustedKey{"host-linux-test": {
		KeyID: "host-linux-test", Role: SignerRoleHostAttestation, HostID: "sandbox-host", PublicKey: hostPrivate.Public().(ed25519.PublicKey),
	}}
	if _, err := VerifyEnvelope(hostRecord, foreignHostKey); !errors.Is(err, ErrSignerRoleMismatch) {
		t.Fatalf("foreign host key binding error = %v", err)
	}
}

func TestLanesL301AuthoritySuccessorRequiresPreviousTrustedKey(t *testing.T) {
	key := testSigningKey(0)
	if _, err := SignEnvelope("authority_key_set", []byte(`{"previous_key_id":"other-authority","vector":1}`), SigningIdentity{
		KeyID: "authority-test", Role: SignerRolePreviousAuthorityKey, PrivateKey: key,
	}); !errors.Is(err, ErrSignerRoleMismatch) {
		t.Fatalf("wrong predecessor key ID error = %v", err)
	}
	valid, err := SignEnvelope("authority_key_set", []byte(`{"previous_key_id":"authority-test","vector":1}`), SigningIdentity{
		KeyID: "authority-test", Role: SignerRolePreviousAuthorityKey, PrivateKey: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	previousTrustedKey := map[string]TrustedKey{"authority-test": {
		KeyID: "authority-test", Role: SignerRoleReleaseAuthority, PublicKey: key.Public().(ed25519.PublicKey),
	}}
	if _, err := VerifyEnvelope(valid, previousTrustedKey); err != nil {
		t.Fatalf("current trusted authority could not sign its successor key set: %v", err)
	}
	wrongRole := map[string]TrustedKey{"authority-test": {
		KeyID: "authority-test", Role: SignerRoleHostAttestation, HostID: "linux-host", PublicKey: key.Public().(ed25519.PublicKey),
	}}
	if _, err := VerifyEnvelope(valid, wrongRole); !errors.Is(err, ErrSignerRoleMismatch) {
		t.Fatalf("successor signed by host-role key error = %v", err)
	}
}

func TestLanesL301RejectsTamperedIDSignatureAndMalformedEnvelope(t *testing.T) {
	key := testSigningKey(0)
	valid, err := SignEnvelope("candidate_manifest", []byte(`{"scope":"authority","vector":1}`), SigningIdentity{
		KeyID: "authority-test", Role: SignerRoleReleaseAuthority, PrivateKey: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	trusted := map[string]TrustedKey{"authority-test": {KeyID: "authority-test", Role: SignerRoleReleaseAuthority, PublicKey: key.Public().(ed25519.PublicKey)}}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(valid, &fields); err != nil {
		t.Fatal(err)
	}
	fields["record_id"] = json.RawMessage(`"0000000000000000000000000000000000000000000000000000000000000000"`)
	tamperedID, _ := json.Marshal(fields)
	tamperedID, _ = CanonicalizeJSON(tamperedID)
	if _, err := VerifyEnvelope(tamperedID, trusted); !errors.Is(err, ErrRecordIDMismatch) {
		t.Fatalf("tampered ID error = %v, want ErrRecordIDMismatch", err)
	}
	if err := json.Unmarshal(valid, &fields); err != nil {
		t.Fatal(err)
	}
	fields["signature"] = json.RawMessage(`"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=="`)
	tamperedSignature, _ := json.Marshal(fields)
	tamperedSignature, _ = CanonicalizeJSON(tamperedSignature)
	if _, err := VerifyEnvelope(tamperedSignature, trusted); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("tampered signature error = %v, want ErrInvalidSignature", err)
	}
	if _, err := VerifyEnvelope([]byte(`{"release_record_version":1}`), trusted); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("truncated envelope error = %v, want ErrInvalidEnvelope", err)
	}
	if _, err := VerifyEnvelope([]byte(`{"payload":{"x":"\ud800"}}`), trusted); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("invalid Unicode envelope error = %v, want ErrInvalidEnvelope", err)
	}
	if strings.Contains(string(valid), " ") {
		t.Fatalf("builder inserted whitespace in canonical envelope: %s", valid)
	}
}

func testSigningKey(start byte) ed25519.PrivateKey {
	seed := make([]byte, ed25519.SeedSize)
	for index := range seed {
		seed[index] = start + byte(index)
	}
	return ed25519.NewKeyFromSeed(seed)
}
