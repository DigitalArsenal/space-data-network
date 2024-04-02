package main

import (
	"encoding/csv"
	"fmt"
	"log"
	"os"
	"path"
	"strconv"

	OMM "github.com/DigitalArsenal/space-data-network/internal/spacedatastandards/OMM" // Replace with your actual OMM package path
	serverconfig "github.com/DigitalArsenal/space-data-network/serverconfig"
	flatbuffers "github.com/google/flatbuffers/go"
)

var buf []byte

func main() {
	serverconfig.Init()

	outgoingFilePath := path.Join(serverconfig.Conf.Folders.OutgoingFolder, "omm_data.fb")
	// Remove the file if it exists
	if err := os.Remove(outgoingFilePath); err != nil && !os.IsNotExist(err) {
		log.Fatalf("Error removing outgoing file: %v", err)
	}

	csvFile, err := os.Open("./test/catalog.csv")
	if err != nil {
		log.Fatalf("Error opening CSV file: %v", err)
	}
	defer csvFile.Close()

	reader := csv.NewReader(csvFile)
	// Assuming the first row is headers and skipping it
	_, err = reader.Read()
	if err != nil {
		log.Fatalf("Error reading header row: %v", err)
	}

	outgoingFile, err := os.OpenFile(outgoingFilePath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0644)
	if err != nil {
		log.Fatalf("Error opening outgoing file: %v", err)
	}
	defer outgoingFile.Close()

	iterations := 1

	for i := 0; i < iterations; i++ {
		record, err := reader.Read()
		if err != nil {
			break // End of file or an error
		}

		builder := flatbuffers.NewBuilder(0)

		objectName := builder.CreateString(record[0])
		objectId := builder.CreateString(record[1])
		epoch := builder.CreateString(record[2])

		meanMotion, _ := strconv.ParseFloat(record[3], 64)
		eccentricity, _ := strconv.ParseFloat(record[4], 64)
		inclination, _ := strconv.ParseFloat(record[5], 64)
		raOfAscNode, _ := strconv.ParseFloat(record[6], 64)
		argOfPericenter, _ := strconv.ParseFloat(record[7], 64)
		meanAnomaly, _ := strconv.ParseFloat(record[8], 64)

		// Assuming EPHEMERIS_TYPE and CLASSIFICATION_TYPE are strings in the CSV
		ephemerisType := OMM.EnumValuesephemerisType["SGP4"]
		classificationType := builder.CreateString(record[10])

		noradCatId, _ := strconv.ParseUint(record[11], 10, 32)
		elementSetNo, _ := strconv.ParseUint(record[12], 10, 32)
		revAtEpoch, _ := strconv.ParseFloat(record[13], 64)
		bstar, _ := strconv.ParseFloat(record[14], 64)
		meanMotionDot, _ := strconv.ParseFloat(record[15], 64)
		meanMotionDdot, _ := strconv.ParseFloat(record[16], 64)

		OMM.OMMStart(builder)

		OMM.OMMAddOBJECT_NAME(builder, objectName)
		OMM.OMMAddOBJECT_ID(builder, objectId)
		OMM.OMMAddEPOCH(builder, epoch)
		OMM.OMMAddMEAN_MOTION(builder, meanMotion)
		OMM.OMMAddECCENTRICITY(builder, eccentricity)
		OMM.OMMAddINCLINATION(builder, inclination)
		OMM.OMMAddRA_OF_ASC_NODE(builder, raOfAscNode)
		OMM.OMMAddARG_OF_PERICENTER(builder, argOfPericenter)
		OMM.OMMAddMEAN_ANOMALY(builder, meanAnomaly)

		OMM.OMMAddEPHEMERIS_TYPE(builder, ephemerisType)
		OMM.OMMAddCLASSIFICATION_TYPE(builder, classificationType)
		OMM.OMMAddNORAD_CAT_ID(builder, uint32(noradCatId))
		OMM.OMMAddELEMENT_SET_NO(builder, uint32(elementSetNo))
		OMM.OMMAddREV_AT_EPOCH(builder, revAtEpoch)
		OMM.OMMAddBSTAR(builder, bstar)
		OMM.OMMAddMEAN_MOTION_DOT(builder, meanMotionDot)
		OMM.OMMAddMEAN_MOTION_DDOT(builder, meanMotionDdot)

		omm := OMM.OMMEnd(builder)

		builder.FinishSizePrefixedWithFileIdentifier(omm, []byte("$OMM"))
		//builder.FinishWithFileIdentifier(omm, []byte("$OMM"))
		//builder.FinishSizePrefixed(omm)
		// buf contains the serialized data for the current record
		buf = builder.FinishedBytes()
		if _, err := outgoingFile.Write(buf); err != nil {
			log.Fatalf("Error writing to outgoing file: %v", err)
		}
	}
	sizeOfBuf := flatbuffers.GetSizePrefix(buf, 0)
	fileID := string(buf[8:12])

	fmt.Println("FID: ", fileID)
	fmt.Println("Buf Size: ", sizeOfBuf)
	fmt.Println("Written to: ", outgoingFilePath)

}
