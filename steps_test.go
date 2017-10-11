package httpsim

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStringBetweenN(t *testing.T) {
	body := `
	-[0]
	-[1]
	-[2]
	-[3]
	-[4]
	-[5]
	-[6]
	-[7]
	-[8]
	-[9]
	-[10]
	`
	bef := "-["
	aft := "]\n"

	for i := 0; i <= 10; i++ {
		// exp :=strconv.Itoa(i)
		_, str := stringBetweenN(body, bef, aft, i)
		assert.Equal(t, strconv.Itoa(i), str)
	}
}

func TestExtractable_Extract(t *testing.T) {
	body := `
		-[ASD12]
		-[asd123]
		-[asdfffadsfasdfasfasdfHTMLMTMHTLHMTHMLTHTMLTHMTL]
		-[a]
		-[ASDfff]
		-[asdfff]
	`
	ex := Extractable{
		AfterThis:  "-[",
		BeforeThis: "]",
		Name:       "some string",

		Iterate:     true,
		MaxLength:   10,
		MinLength:   3,
		MatchRegexp: "[a-z]+",
	}
	name, value, err := ex.Extract(body, nil)
	assert.Nil(t, err)
	assert.Equal(t, "some string", name)
	assert.Equal(t, "asdfff", value)
}
