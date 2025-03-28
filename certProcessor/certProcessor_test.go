package certprocessor

import (
	"testing"
	"unicode"
)

func clusterCACert() string {
	clusterCACert := `-----BEGIN CERTIFICATE-----
MIIFLTCCAxWgAwIBAgIUYThZW2C1f6pLM6JbgsuQusZDpKUwDQYJKoZIhvcNAQEN
BQAwLTETMBEGA1UECgwKaW8uc3RyaW16aTEWMBQGA1UEAwwNY2x1c3Rlci1jYSB2
MDAeFw0yMTEwMTkyMDQ4NDFaFw0yMjEwMTkyMDQ4NDFaMC0xEzARBgNVBAoMCmlv
LnN0cmltemkxFjAUBgNVBAMMDWNsdXN0ZXItY2EgdjAwggIiMA0GCSqGSIb3DQEB
AQUAA4ICDwAwggIKAoICAQDPEjmS0/K6o+o9zLwYNo7WMeHLCsHnbw8aljDBUXKR
duftlQlIUQGJC22AH/kmxaHarIAZGhvZtSSpBM662oI9DGL/5Vt7ASvlzch2U5Rx
NHk3R8+Whsnc6UzZsKxvpronoG638UXi7g8nbCIJUUzTjwp/71T1jugfG6cbZPfs
Mj955pM3RNT8JNlLSU5LUj/DEU+HZfsAVYULUl70CYhHFo9yVA4cag2/wBXr5ejQ
oVi5TpFFcnA6Qi0kcECbdX7Tt99MBXcMd2Hc8Rw/nZZYF5oiRYGp0mlMqS/ev1zJ
OTN5Wg8qxPKaz3XocaEMwT+3hLehNEVy8KMsr2fsgiREUYrXjMppnkBLELq6p6uM
kmzvJ8IPbOUA/crnfVGWGNtsOVUCGVOdJA0KHEIlqiZbk5LWWBGwniBEs7rHA/Pm
6eCFmwhkyX/A3IvluxWksyTKlUg/RLLY88H3RWEnf+PMScFO58wnA0jfZ9+e5fRw
b9ozOk+V5vc9AZB0PdJzC0wRzJN3pERC77zrJrGOGFRvgz6Bg86LoWhzH3D2TGpF
49eNJS9NPgdOC7u2y7WvgkR2Q1TvHndDrm+ZPw+gcBuJB234d+zBY8KQSKOE4qo1
zveJCuaaTe9yT/bTEcrL4gipbJ3i2t9tCUkED8AJepXAzT9jXcesGH9KQ9v4f0Sk
awIDAQABo0UwQzAdBgNVHQ4EFgQU1wq9E8qWO93Ln1brxd0/AxmsPt0wEgYDVR0T
AQH/BAgwBgEB/wIBADAOBgNVHQ8BAf8EBAMCAQYwDQYJKoZIhvcNAQENBQADggIB
AM5/4tYT4X9f29HlYhBNooH394cKUfrodObFHYIywh20Mah7iyw2Os12OxyzKNVp
YjTTgPFHl0KmExOUglJvSorY2TVfiUiq7QzT5XwRnosmSryqMR8B/vMyiQEn1of+
vEID4k+eB8bdFlQCV6xmyR3swcwM0aOpne29D6x5p/ilOknXNA2xlxUzF470Awvm
NbLHW1ODtyDlvHS3eS6ofN3Sf2nR4r8i8fF/NceL+VDnVOl0sLAj+awgNBeEuUQl
zLCmcgqmugFT2haxOmr9IDlXNXSI8YFqEfE2nHgLo17lq1/rDThi78PXlgZpprT4
1hpEocD/t+6nJePQVzzV1CyTihcCRwVYbSI+T4Z2c+FYvc5vdiQJ5hoS7QcI1ns4
vbgyxXpw4Wz+/3IAga6qjsYUb6DN70JmLy3pwB2aVDN8lvmxlOvhtkJcDBkgFCLc
9LGEh/CHonDIYG6st43Te+fzJk2B8IO8nllOZMh1aAX0/9yNXco/2E7R6DKuhnj4
8YxwugJcZ7iydK+giatSlF059AD5nqk0wp4CQxw6nhQcJsGxMHfmSl6tlZwhUJrV
SnJ2auomP4fc6elwr9LCBOl1HpBZ5XkijO7WLL1zCGBDYIu45Ruy16BYN1EWqnjJ
sr7xIs5yJ9mzWcjYQ/9XJkIh5j0whhVFB17W9bzEnTjs
-----END CERTIFICATE-----
`
	return clusterCACert
}

func userCACert() string {
	userCACert := `-----BEGIN CERTIFICATE-----
MIIFFzCCAv+gAwIBAgIUJOLkz7zytAO1u4rGiLsg+dyiWcIwDQYJKoZIhvcNAQEL
BQAwGjEYMBYGA1UEAwwPU1RSSU1aSS1TUi1URVNUMCAXDTI1MDMyODEyMzgyNFoY
DzIwNTIwODEzMTIzODI0WjAaMRgwFgYDVQQDDA9TVFJJTVpJLVNSLVRFU1QwggIi
MA0GCSqGSIb3DQEBAQUAA4ICDwAwggIKAoICAQC67S6hAyAGLAtWPPeAsIFWZf+5
LK16A2cd4QlUgOhzvTPm7x6MQVhQi0fNpReKytZ4B9yDn7/sjeu6Au3oqszIw2sF
lAjrP5U5aAb93WjfZnvjU0xP9pyeb7REiBkR6iwhL+fj4CieAtc6b/fx9Qi6Mg18
Si9vKIF193jHAcvEBfvPDGtZCq1MTtIlqHN37buHGkjLTmxjzWM6MwQHU11RvuP8
r7xRyVPPkTqAo2bW+rxRlo0J/hbBZbkcSZ0OxrH9P7JsqdlCHITksE2mh2A6MpB6
NyTV/LiEZjIC4YMIK+/X1UO/lS7OxLq2kRbIj2UoIxJnoCTxP1Zzr123uP65cNt4
vkXgrQgprc4ijBVB3fyicwIG8CJWXLqkjuZ3XYgeTdufMUfFnICUKMkjXC/24QyX
6VFY3qtjc/iI31rFak5w4GLbH4LA0GjzMChNDKX4RAhL+D268YTr8eVIbG9OhYtJ
zBA6NAter9RVGpoCJ+jpLrVWfnabm3qlIUOHhdTg+a2ruRyZZM4HNuFLTSCq3Wa1
JNN/lrub55ZXfg35pgplz8vKhAmuwf91Tfh/gVyNyDp6p+F9xSdk2wteMYQG9ufm
j6QQq/9KVq9p4ua+SblVm+uaiv0cu2C/JLm4EwUC2fk1aJaZF2OwNlVwf1eDuMVp
tmvcqXQ4GzR8ZncctwIDAQABo1MwUTAdBgNVHQ4EFgQUkj0W4Z8/3ehvi8ZoIJqg
XoHxALkwHwYDVR0jBBgwFoAUkj0W4Z8/3ehvi8ZoIJqgXoHxALkwDwYDVR0TAQH/
BAUwAwEB/zANBgkqhkiG9w0BAQsFAAOCAgEACU7Q3lK64I0S/rfkPinKk43RXdAa
dh1ecs+FSgw5fkYGq6hH5WnSwG5ftBgrfqtKccFL++0kXFlssRHe0XwgZOU6mq6Y
Ibwug7PVhliJWPUAmkImBxx+2nEcbKe8/aQhJdYU42F/iKM2eu0lpuxpCGTxjnWX
MmbqlYo6+sZdyv3d51lqR6H2vj/kWx3UE5hFqL+9f+yq1j9bzizZPHUz/AG9wsds
ZDkIOH9Rt/+qRrKebSMVVK2R0AwMVfq6FLP6jjLcxsemAtLnRGDkKLBnq8RMZSKj
jSr/DvkdnmVmM+Q9OoliSfqrXq4bReKr9D1sjIFXhW+snfRbWP/x5+lkBUzOGGln
5lUo8i4P6cck5dyW0X16aDTQXuUd91AR00ZzTUNAq3YrPrtflPi6NcmJVfab2Ogh
hAf+PebfSSv2rZ5RtQo28P1YaegerzdHLaIpofGP2RwQ5AWNNB3zKpvXZ5ysRN52
2jGal+H4mnpWqznb8ynCNki6zIanWofbKulplj0a8TooT1OBJap+U9l4QLXNDiVN
aR9Jf0P9e3xtBnNeIVUON2aOQ5PTw5VdLVlo3umBWGQ7odSbOmvTnJkqA7psmVOR
DUagf2U5O1GIuJmTgz2yrD1kpZis79k6VTvc+QIQdUFa15Pgk4QfnPszT0PJlfg0
mmY6wv33AVS6Ok0=
-----END CERTIFICATE-----
`
	return userCACert
}

func userCert() string {
	userCert := `-----BEGIN CERTIFICATE-----
MIIExzCCAq8CFC0mSjQoIueh2CgTnAseeZLzWwUDMA0GCSqGSIb3DQEBCwUAMBox
GDAWBgNVBAMMD1NUUklNWkktU1ItVEVTVDAgFw0yNTAzMjgxMjQzMTBaGA8yMDUy
MDgxMzEyNDMxMFowJDEiMCAGA1UEAwwZY29uZmx1ZW50LXNjaGVtYS1yZWdpc3Ry
eTCCAiIwDQYJKoZIhvcNAQEBBQADggIPADCCAgoCggIBAK/ZwIHy3hzKI9f7mHjZ
ABLAJgV/qu0tmKP/BScu/rG5iNpnYRxcrBcnAuRqN1A5juJ+InDkJ7T8hbeFeO8Z
T6eD+QS6OZ9TwvVC0/RfsH3XugrsI87DgdOmmaEB9ucA5puQzOSNPBUxSENlPRBY
4Dvedp5CsADwl/uAr/P/OtrWdiFjul4eYWLw+gblU37fm3RvadGHdZqaKbUxV2Jd
X8S52mBwoVwU9zPqeeywm5JknzNFnE3FYchLERPs4vMHnFaoJRUiKnczBQoxx+82
Y0rEu5RqXTQ1hGs/FJ3Die+d90dEIEpsysJNqzTHCHqlfhKB+iBWbNzXeFPc6cKU
C6hzcosvtGlSKRUKN9QvlhU30gAfvgl4MVgXQodt5QkB0d/WkYceUbPm5LUbNsLi
8yN6b5sK/dzMPQ4WaJYd2CJsR/Ld7KGbwYt+sNa+/15d4YwRREbAJJdBNcSpdHGO
BnPEwRGHo+lyMfdxBxzsGAevZGwClCOzXPUgQ/NETr8WNgTX4kaaAKxWI6olIynG
fWrsluD3Rn8TzTrNO7lc9xdy72zHlzutERhkstDM2wG6KvDMLs9pW7MJaWNpNrvN
/kMwem0phTedmUnpK/FjW6rS+5RemFWb8QKQgaiZvawtSGQwEZJclQGHeoP4K9E/
DML3H8S3/r2iZ3w02al5OIoFAgMBAAEwDQYJKoZIhvcNAQELBQADggIBAFRIdTI7
erMwNUM1D6FlOgGzLcvsv8EiEl8y/nFvzjyB5Bh29o5j3x0nuBkHmmncyduCtzKh
CsBhfZFm4Wj4HZ5CzII7yHv9xhxcrsj3KTChFAWL0gIyyxqcS1BlcKvmtovp6rna
O3LpmNhcqpaI8uIQUiJvglZMqvnoSj7xJGHMcxz+7F5klQ8GTuNR0SB/Ix3K4+xO
Jeg4wkjGyajGLGgOxrr6TkB2C6W3EfVsuMB6ZeD00EA4j/x+x86FWDm5fhx1fS95
4kzD50EZhpCX5qZy7psOB3gDw/Gpv3frX2BcvA3+1VAtOvkI2r8NfJRwJ5iEQOz4
I+9Z5ecK2y5elCVYFkc6b5UOLgSsBdVoUGbB8qGDNLSzV0g+8VvvGSVy7G8Np6EI
HA4vwnR9TqDCoPaGmSkKMrhplAuvz/dStiDQ/21CDmA5lPIMFH3Bz4PjyBJDuzPI
bxvCWY3ka5CYCaqxF4Ddf7iryaqD2yLHUhjMQ3Dq8iGd97KWAexztPIqfnvgTAxb
nkMzjF2wwBjPttrtdrtmOBF/h5YtyS7JrYxSRsuuJVl+lpovWgCbtrfO8uPW+Zs+
Y0epjjmxKnzTAYCNq2NftqmMShZBy3oF6J4DmyATPPX+p7LfokfkjPc1y6qdZ+8e
31YQPTwkZsk9uReD6gzHB4IjNv3tUKuddP7v
-----END CERTIFICATE-----
`
	return userCert
}

func userKey() string {
	userKey := `-----BEGIN PRIVATE KEY-----
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
1Rs/ntEbB2/Xb9wi5ez3s3thUz/n
-----END PRIVATE KEY-----
`
	return userKey
}
func TestGeneratePasswordLenght(t *testing.T) {
	expectedLenght := 24
	password := GeneratePassword(24, true, false)

	if len(password) != expectedLenght {
		t.Errorf("Output %q not equal to expected %q", len(password), expectedLenght)
		return
	}
	t.Log("Password lenght correct")
}

func TestGeneratePasswordLetter(t *testing.T) {
	result := true
	var badSymbol rune
	password := GeneratePassword(12, false, false)
	for _, r := range password {
		if !unicode.IsLetter(r) {
			result = false
			badSymbol = r
		}
	}
	if !result {
		t.Errorf("Output has not only letter. Bad- %s", string(badSymbol))
		return
	}
	t.Log("Password consist of letters")
}

func TestGeneratePasswordNumber(t *testing.T) {
	result := true
	var badSymbol rune
	password := GeneratePassword(12, true, false)
	for _, r := range password {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			result = false
			badSymbol = r
		}
	}
	if !result {
		t.Errorf("Output has not only letter and digits. Bad - %s", string(badSymbol))
		return
	}
	t.Log("Password consist of letters and digits")
}

func TestGeneratePasswordSpecial(t *testing.T) {
	result := false
	password := GeneratePassword(12, true, true)
	for _, r := range password {
		if !unicode.IsLetter(r) || !unicode.IsDigit(r) {
			result = true
		}
	}
	if !result {
		t.Error("No special symbol in password")
		return
	}
	t.Log("Password has special symbol")
}

func TestCreate_truststore(t *testing.T) {
	truststore, password, err := CreateTruststore(clusterCACert(), "test1234")
	if err != nil {
		t.Error(err)
	}
	if len(truststore) <= 0 {
		t.Error("Empty Truststore")
		return
	}
	if password != "test1234" {
		t.Errorf("Password no equal. Want - %s, got - %s", "test1234", password)
		return
	}
	t.Log("Trustore is created")
}

func TestCreateKeystore(t *testing.T) {
	keystore, password, err := CreateKeystore(userCACert(), userCert(), userKey(), "", "test1234")
	if err != nil {
		t.Error(err)
	}
	if len(keystore) <= 0 {
		t.Error("Empty Keystore")
		return
	}
	if password != "test1234" {
		t.Errorf("Password no equal. Want - %s, got - %s", "test1234", password)
		return
	}
	t.Log("Keytore is created")
}
