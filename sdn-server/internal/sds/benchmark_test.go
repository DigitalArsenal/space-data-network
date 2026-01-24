package sds

import (
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/CAT"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/EPM"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"
)

// BenchmarkOMMSerialization measures OMM creation throughput
func BenchmarkOMMSerialization(b *testing.B) {
	builder := NewOMMBuilder().
		WithObjectName("ISS (ZARYA)").
		WithObjectID("1998-067A").
		WithNoradCatID(25544).
		WithEpoch("2024-01-15T12:00:00.000Z").
		WithMeanMotion(15.49).
		WithEccentricity(0.0001215).
		WithInclination(51.6434)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = builder.Build()
	}
}

// BenchmarkOMMDeserialization measures OMM parsing throughput
func BenchmarkOMMDeserialization(b *testing.B) {
	data := NewOMMBuilder().
		WithObjectName("ISS (ZARYA)").
		WithNoradCatID(25544).
		Build()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		omm := OMM.GetSizePrefixedRootAsOMM(data, 0)
		_ = omm.OBJECT_NAME()
		_ = omm.NORAD_CAT_ID()
		_ = omm.MEAN_MOTION()
		_ = omm.ECCENTRICITY()
		_ = omm.INCLINATION()
	}
}

// BenchmarkOMMFullAccess measures complete field access
func BenchmarkOMMFullAccess(b *testing.B) {
	data := NewOMMBuilder().Build()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		omm := OMM.GetSizePrefixedRootAsOMM(data, 0)
		_ = omm.OBJECT_NAME()
		_ = omm.OBJECT_ID()
		_ = omm.NORAD_CAT_ID()
		_ = omm.EPOCH()
		_ = omm.MEAN_MOTION()
		_ = omm.ECCENTRICITY()
		_ = omm.INCLINATION()
		_ = omm.RA_OF_ASC_NODE()
		_ = omm.ARG_OF_PERICENTER()
		_ = omm.MEAN_ANOMALY()
		_ = omm.CENTER_NAME()
		_ = omm.CREATION_DATE()
		_ = omm.ORIGINATOR()
	}
}

// BenchmarkEPMSerialization measures EPM creation throughput
func BenchmarkEPMSerialization(b *testing.B) {
	builder := NewEPMBuilder().
		WithDN("John Doe").
		WithLegalName("Acme Corporation").
		WithFamilyName("Doe").
		WithGivenName("John").
		WithEmail("john.doe@acme.com").
		WithTelephone("+1-555-0100").
		WithAddress("456 Main St", "Springfield", "IL", "62701", "USA")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = builder.Build()
	}
}

// BenchmarkEPMDeserialization measures EPM parsing throughput
func BenchmarkEPMDeserialization(b *testing.B) {
	data := NewEPMBuilder().Build()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		epm := EPM.GetSizePrefixedRootAsEPM(data, 0)
		_ = epm.DN()
		_ = epm.LEGAL_NAME()
		_ = epm.FAMILY_NAME()
		_ = epm.GIVEN_NAME()
		_ = epm.EMAIL()
		_ = epm.TELEPHONE()
	}
}

// BenchmarkEPMWithKeys measures EPM with cryptographic keys
func BenchmarkEPMWithKeys(b *testing.B) {
	data := NewEPMBuilder().
		WithKeys("0xsigningkey123", "0xencryptionkey456").
		Build()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		epm := EPM.GetSizePrefixedRootAsEPM(data, 0)
		key := new(EPM.CryptoKey)
		for j := 0; j < epm.KEYSLength(); j++ {
			if epm.KEYS(key, j) {
				_ = key.PUBLIC_KEY()
				_ = key.KEY_TYPE()
			}
		}
	}
}

// BenchmarkPNMSerialization measures PNM creation throughput
func BenchmarkPNMSerialization(b *testing.B) {
	builder := NewPNMBuilder().
		WithMultiformatAddress("/ip4/192.168.1.1/tcp/4001/p2p/QmTestPeerID123").
		WithCID("bafybeiabcdef1234567890testcid").
		WithFileID("OMM.fbs").
		WithSignature("0xsignature123abc")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = builder.Build()
	}
}

// BenchmarkPNMDeserialization measures PNM parsing throughput
func BenchmarkPNMDeserialization(b *testing.B) {
	data := NewPNMBuilder().Build()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pnm := PNM.GetSizePrefixedRootAsPNM(data, 0)
		_ = pnm.CID()
		_ = pnm.FILE_ID()
		_ = pnm.MULTIFORMAT_ADDRESS()
		_ = pnm.SIGNATURE()
	}
}

// BenchmarkCATSerialization measures CAT creation throughput
func BenchmarkCATSerialization(b *testing.B) {
	builder := NewCATBuilder().
		WithObjectName("ISS (ZARYA)").
		WithObjectID("1998-067A").
		WithNoradCatID(25544).
		WithOrbitalParams(92.9, 51.6, 420.0, 418.0)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = builder.Build()
	}
}

// BenchmarkCATDeserialization measures CAT parsing throughput
func BenchmarkCATDeserialization(b *testing.B) {
	data := NewCATBuilder().Build()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cat := CAT.GetSizePrefixedRootAsCAT(data, 0)
		_ = cat.OBJECT_NAME()
		_ = cat.NORAD_CAT_ID()
		_ = cat.PERIOD()
		_ = cat.INCLINATION()
		_ = cat.APOGEE()
		_ = cat.PERIGEE()
	}
}

// BenchmarkSchemaComparison compares throughput across schemas
func BenchmarkSchemaComparison(b *testing.B) {
	b.Run("OMM/Serialize", func(b *testing.B) {
		builder := NewOMMBuilder()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = builder.Build()
		}
	})

	b.Run("OMM/Deserialize", func(b *testing.B) {
		data := NewOMMBuilder().Build()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			omm := OMM.GetSizePrefixedRootAsOMM(data, 0)
			_ = omm.OBJECT_NAME()
		}
	})

	b.Run("EPM/Serialize", func(b *testing.B) {
		builder := NewEPMBuilder()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = builder.Build()
		}
	})

	b.Run("EPM/Deserialize", func(b *testing.B) {
		data := NewEPMBuilder().Build()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			epm := EPM.GetSizePrefixedRootAsEPM(data, 0)
			_ = epm.DN()
		}
	})

	b.Run("PNM/Serialize", func(b *testing.B) {
		builder := NewPNMBuilder()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = builder.Build()
		}
	})

	b.Run("PNM/Deserialize", func(b *testing.B) {
		data := NewPNMBuilder().Build()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			pnm := PNM.GetSizePrefixedRootAsPNM(data, 0)
			_ = pnm.CID()
		}
	})

	b.Run("CAT/Serialize", func(b *testing.B) {
		builder := NewCATBuilder()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = builder.Build()
		}
	})

	b.Run("CAT/Deserialize", func(b *testing.B) {
		data := NewCATBuilder().Build()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			cat := CAT.GetSizePrefixedRootAsCAT(data, 0)
			_ = cat.OBJECT_NAME()
		}
	})
}

// BenchmarkMessageSize reports message sizes for different schemas
func BenchmarkMessageSize(b *testing.B) {
	ommData := NewOMMBuilder().Build()
	epmData := NewEPMBuilder().Build()
	pnmData := NewPNMBuilder().Build()
	catData := NewCATBuilder().Build()

	b.Run("OMM", func(b *testing.B) {
		b.ReportMetric(float64(len(ommData)), "bytes")
		for i := 0; i < b.N; i++ {
			_ = NewOMMBuilder().Build()
		}
	})

	b.Run("EPM", func(b *testing.B) {
		b.ReportMetric(float64(len(epmData)), "bytes")
		for i := 0; i < b.N; i++ {
			_ = NewEPMBuilder().Build()
		}
	})

	b.Run("PNM", func(b *testing.B) {
		b.ReportMetric(float64(len(pnmData)), "bytes")
		for i := 0; i < b.N; i++ {
			_ = NewPNMBuilder().Build()
		}
	})

	b.Run("CAT", func(b *testing.B) {
		b.ReportMetric(float64(len(catData)), "bytes")
		for i := 0; i < b.N; i++ {
			_ = NewCATBuilder().Build()
		}
	})
}

// BenchmarkBatchProcessing measures batch message processing
func BenchmarkBatchProcessing(b *testing.B) {
	// Create a batch of 100 OMM messages
	messages := make([][]byte, 100)
	builder := NewOMMBuilder()
	for i := 0; i < 100; i++ {
		messages[i] = builder.WithNoradCatID(uint32(i + 1)).Build()
	}

	b.Run("Deserialize100", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			for _, msg := range messages {
				omm := OMM.GetSizePrefixedRootAsOMM(msg, 0)
				_ = omm.NORAD_CAT_ID()
			}
		}
	})

	b.Run("Serialize100", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			for j := 0; j < 100; j++ {
				_ = builder.WithNoradCatID(uint32(j + 1)).Build()
			}
		}
	})
}

// BenchmarkParallelProcessing measures parallel message processing
func BenchmarkParallelProcessing(b *testing.B) {
	data := NewOMMBuilder().Build()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			omm := OMM.GetSizePrefixedRootAsOMM(data, 0)
			_ = omm.OBJECT_NAME()
			_ = omm.NORAD_CAT_ID()
		}
	})
}
