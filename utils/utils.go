package utils

import (
	"encoding/json"
	"io/ioutil"
	"net/http"
	"time"

	"github.com/iotaledger/giota"
)

func getJSON(url string, target interface{}) error {
	var myClient = &http.Client{Timeout: 10 * time.Second}

	resp, err := myClient.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	result, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	err = json.Unmarshal(result, target)
	return err
}

func ReadNodeList(address string) ([]string, error) {
	var nodeList []string
	err := getJSON(address, &nodeList)
	return nodeList, err
}

func FromString(s string) giota.Trytes {
	Alphabet := "9ABCDEFGHIJKLMNOPQRSTUVWXYZ"

	var output string
	chars := []rune(s)

	for _, c := range chars {
		v1 := c % 27
		v2 := (c - v1) / 27
		output += string(Alphabet[v1]) + string(Alphabet[v2])
	}
	return giota.Trytes(output)
}
