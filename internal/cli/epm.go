package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	flatbuffer_utils "path/to/your/flatbuffer_utils/package" // Replace with the actual import path.
	// EPM "github.com/DigitalArsenal/space-data-network/internal/spacedatastandards/EPM"
)

func CreateServerEPM() {
	var epmBytes []byte
	var confirmed string
	reader := bufio.NewReader(os.Stdin)

	for {
		fmt.Println("Creating a server EPM...")

		// Read input for all fields
		dnString, _ := readInput(reader, "Enter DN components (e.g., 'CN=John Doe, O=Example Corp, OU=IT Dept, DC=example, DC=com'): ")
		legalName, _ := readInput(reader, "Enter legal name: ")
		familyName, _ := readInput(reader, "Enter family name: ")
		givenName, _ := readInput(reader, "Enter given name: ")
		additionalName, _ := readInput(reader, "Enter additional name: ")
		honorificPrefix, _ := readInput(reader, "Enter honorific prefix: ")
		honorificSuffix, _ := readInput(reader, "Enter honorific suffix: ")
		jobTitle, _ := readInput(reader, "Enter job title: ")
		occupation, _ := readInput(reader, "Enter occupation: ")
		altNamesInput, _ := readInput(reader, "Enter alternate names (comma-separated): ")
		email, _ := readInput(reader, "Enter email: ")
		telephone, _ := readInput(reader, "Enter telephone: ")
		multiformatAddressesInput, _ := readInput(reader, "Enter multiformat addresses (comma-separated): ")

		// Parse comma-separated alternate names and multiformat addresses
		alternateNames := parseInput(altNamesInput)
		multiformatAddresses := parseInput(multiformatAddressesInput)

		// Call the flatbuffer_utils.CreateEPM with the collected data
		epmBytes = flatbuffer_utils.CreateEPM(
			dnString,
			legalName,
			familyName,
			givenName,
			additionalName,
			honorificPrefix,
			honorificSuffix,
			jobTitle,
			occupation,
			alternateNames,
			email,
			telephone,
			multiformatAddresses,
		)

		// Print out the EPM data for confirmation
		printEPM(dnString, legalName, familyName, givenName, additionalName, honorificPrefix, honorificSuffix, jobTitle, occupation, alternateNames, email, telephone, multiformatAddresses)

		// Ask if the EPM data is correct
		confirmed, _ = readInput(reader, "Is the above information correct? (Y/N): ")
		if strings.ToUpper(confirmed) == "Y" {
			break
		}
	}

	// Handle the confirmed EPM bytes, such as saving them to a file or sending over a network.
	fmt.Println("EPM created successfully. Length of EPM bytes:", len(epmBytes))
	// Example: ioutil.WriteFile("path/to/epm/file", epmBytes, 0644)
}

func readInput(reader *bufio.Reader, prompt string) (string, error) {
	fmt.Print(prompt)
	input, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(input), nil
}

func parseInput(input string) []string {
	input = strings.TrimSpace(input)
	if input == "" {
		return []string{}
	}
	return strings.Split(input, ",")
}

func printEPM(dnString, legalName, familyName, givenName, additionalName, honorificPrefix, honorificSuffix, jobTitle, occupation string, alternateNames, email, telephone, multiformatAddresses []string) {
	fmt.Printf("DN: %s\n", dnString)
	fmt.Printf("Legal Name: %s\n", legalName)
	fmt.Printf("Family Name: %s\n", familyName)
	fmt.Printf("Given Name: %s\n", givenName)
	fmt.Printf("Additional Name: %s\n", additionalName)
	fmt.Printf("Honorific Prefix: %s\n", honorificPrefix)
	fmt.Printf("Honorific Suffix: %s\n", honorificSuffix)
	fmt.Printf("Job Title: %s\n", jobTitle)
	fmt.Printf("Occupation: %s\n", occupation)
	fmt.Printf("Alternate Names: %s\n", strings.Join(alternateNames, ", "))
	fmt.Printf("Email: %s\n", email)
	fmt.Printf("Telephone: %s\n", telephone)
	fmt.Printf("Multiformat Addresses: %s\n", strings.Join(multiformatAddresses, ", "))
}
