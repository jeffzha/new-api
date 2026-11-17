package model

import (
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

type cnIntegrityWriterMembership struct {
	WriterID                 string   `json:"writerId"`
	BuildID                  string   `json:"buildId"`
	ProtocolVersion          string   `json:"protocolVersion"`
	CapabilityPaths          []string `json:"capabilityPaths"`
	LeaseGeneration          string   `json:"leaseGeneration"`
	LeaseExpiresAt           string   `json:"leaseExpiresAt"`
	DrainAcknowledgedThrough string   `json:"drainAcknowledgedThrough"`
	MembershipBodyHash       string   `json:"membershipBodyHash,omitempty"`
}

type cnIntegrityFenceBody struct {
	FenceID                  string   `json:"fenceId"`
	Generation               string   `json:"generation"`
	WriterSetHash            string   `json:"writerSetHash"`
	WriterIDs                []string `json:"writerIds"`
	InventoryEvidenceID      string   `json:"inventoryEvidenceId"`
	InventoryEvidenceHash    string   `json:"inventoryEvidenceHash"`
	OldWritersDrained        bool     `json:"oldWritersDrained"`
	RedisDisabled            bool     `json:"redisDisabled"`
	BatchDisabled            bool     `json:"batchDisabled"`
	ExternalSQLExcluded      bool     `json:"externalSqlExcluded"`
	UnguardedHandlesExcluded bool     `json:"unguardedHandlesExcluded"`
	OperatorAttestationID    string   `json:"operatorAttestationId"`
	AttestedAt               string   `json:"attestedAt"`
}

func cnIntegrityMembership(writer CNIntegrityWriter) (cnIntegrityWriterMembership, error) {
	var capabilities []string
	if err := common.Unmarshal([]byte(writer.CapabilityPaths), &capabilities); err != nil {
		return cnIntegrityWriterMembership{}, err
	}
	member := cnIntegrityWriterMembership{WriterID: writer.WriterID, BuildID: writer.BuildID, ProtocolVersion: writer.ProtocolVersion, CapabilityPaths: capabilities, LeaseGeneration: strconv.FormatInt(writer.LeaseGeneration, 10), LeaseExpiresAt: writer.LeaseUntil, DrainAcknowledgedThrough: strconv.FormatInt(writer.DrainAcknowledgedThrough, 10)}
	body, err := common.Marshal(member)
	if err != nil {
		return member, err
	}
	member.MembershipBodyHash, err = CNIntegrityBodyHash(body)
	return member, err
}

func cnIntegrityWriterSetHash(payload []byte) (string, error) {
	var members []cnIntegrityWriterMembership
	if err := common.Unmarshal(payload, &members); err != nil {
		return "", err
	}
	if members == nil {
		return "", errors.New("integrity writer set cannot be null")
	}
	hashes := make([]string, 0, len(members))
	for _, member := range members {
		expected := member.MembershipBodyHash
		member.MembershipBodyHash = ""
		body, err := common.Marshal(member)
		if err != nil {
			return "", err
		}
		hash, err := CNIntegrityBodyHash(body)
		if err != nil || hash != expected {
			return "", errors.New("integrity writer membership hash mismatch")
		}
		hashes = append(hashes, hash)
	}
	slices.Sort(hashes)
	body, err := common.Marshal(hashes)
	if err != nil {
		return "", err
	}
	return CNIntegrityBodyHash(body)
}

func cnIntegrityFenceValid(fence CNIntegrityFence) bool {
	writerHash, writerErr := cnIntegrityWriterSetHash([]byte(fence.WriterSet))
	fenceHash, hashErr := CNIntegrityBodyHash([]byte(fence.FencePayload))
	var body cnIntegrityFenceBody
	var members []cnIntegrityWriterMembership
	if writerErr != nil || hashErr != nil || writerHash != fence.WriterHash || fenceHash != fence.FenceHash || common.Unmarshal([]byte(fence.FencePayload), &body) != nil || common.Unmarshal([]byte(fence.WriterSet), &members) != nil || body.Generation != strconv.FormatInt(fence.Generation, 10) || body.WriterSetHash != writerHash || body.FenceID != CNIdentity("integrity-fence-v2", fence.EpochID, body.Generation) || len(members) == 0 || len(members) != len(body.WriterIDs) || !slices.IsSorted(body.WriterIDs) || !cnIntegrityHashValid(body.InventoryEvidenceHash) || body.InventoryEvidenceID == "" || body.OperatorAttestationID == "" || !body.OldWritersDrained || !body.RedisDisabled || !body.BatchDisabled || !body.ExternalSQLExcluded || !body.UnguardedHandlesExcluded {
		return false
	}
	if _, err := time.Parse(CNTimeLayout, body.AttestedAt); err != nil || body.AttestedAt != fence.AttestedAt {
		return false
	}
	for i, member := range members {
		if member.LeaseGeneration != body.Generation || member.WriterID != body.WriterIDs[i] || (i > 0 && body.WriterIDs[i-1] == member.WriterID) || !cnIntegrityWriterCapabilitiesValid(member.ProtocolVersion, member.CapabilityPaths) {
			return false
		}
	}
	return true
}

func cnPersistIntegrityFenceTx(tx *gorm.DB, epochID string, generation int64, writers []CNIntegrityWriter, attestation CNIntegrityDrainAttestation, attestedAt string) (*CNIntegrityFence, error) {
	slices.SortFunc(writers, func(a, b CNIntegrityWriter) int {
		if a.WriterID < b.WriterID {
			return -1
		}
		if a.WriterID > b.WriterID {
			return 1
		}
		return 0
	})
	members := make([]cnIntegrityWriterMembership, 0, len(writers))
	ids := make([]string, 0, len(writers))
	for i := range writers {
		writers[i].EpochID, writers[i].LeaseGeneration = epochID, generation
		member, err := cnIntegrityMembership(writers[i])
		if err != nil {
			return nil, err
		}
		writers[i].MembershipBodyHash = member.MembershipBodyHash
		members = append(members, member)
		ids = append(ids, member.WriterID)
	}
	payload, err := common.Marshal(members)
	if err != nil {
		return nil, err
	}
	writerHash, err := cnIntegrityWriterSetHash(payload)
	if err != nil {
		return nil, err
	}
	for i := range writers {
		writers[i].SetHash = writerHash
		if err := tx.Create(&writers[i]).Error; err != nil {
			return nil, err
		}
	}
	fenceBody := cnIntegrityFenceBody{FenceID: CNIdentity("integrity-fence-v2", epochID, strconv.FormatInt(generation, 10)), Generation: strconv.FormatInt(generation, 10), WriterSetHash: writerHash, WriterIDs: ids, InventoryEvidenceID: attestation.InventoryID, InventoryEvidenceHash: attestation.InventoryHash, OldWritersDrained: attestation.OldWritersDrained, RedisDisabled: attestation.RedisDisabled, BatchDisabled: attestation.BatchDisabled, ExternalSQLExcluded: attestation.ExternalSQLExcluded, UnguardedHandlesExcluded: attestation.UnguardedHandlesExcluded, OperatorAttestationID: attestation.OperatorEvidenceID, AttestedAt: attestedAt}
	body, err := common.Marshal(fenceBody)
	if err != nil {
		return nil, err
	}
	hash, err := CNIntegrityBodyHash(body)
	if err != nil {
		return nil, err
	}
	attestationBody, err := common.Marshal(attestation)
	if err != nil {
		return nil, err
	}
	fence := CNIntegrityFence{EpochID: epochID, Generation: generation, WriterSet: string(payload), WriterHash: writerHash, FencePayload: string(body), FenceHash: hash, Attestation: string(attestationBody), AttestedAt: attestedAt}
	return &fence, tx.Create(&fence).Error
}

func cnCopyIntegrityFenceTx(tx *gorm.DB, epoch CNIntegrityEpoch, generation int64) (*CNIntegrityFence, error) {
	var previous CNIntegrityFence
	err := lockForUpdate(tx).Where("epoch_id = ? AND generation = ?", epoch.EpochID, epoch.Generation).First(&previous).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var members []cnIntegrityWriterMembership
	if err := common.Unmarshal([]byte(previous.WriterSet), &members); err != nil {
		return nil, err
	}
	var writers []CNIntegrityWriter
	for _, member := range members {
		capabilities, err := common.Marshal(member.CapabilityPaths)
		if err != nil {
			return nil, err
		}
		through, err := strconv.ParseInt(member.DrainAcknowledgedThrough, 10, 64)
		if err != nil {
			return nil, err
		}
		writers = append(writers, CNIntegrityWriter{WriterID: member.WriterID, BuildID: member.BuildID, LeaseUntil: member.LeaseExpiresAt, ProtocolVersion: member.ProtocolVersion, CapabilityPaths: string(capabilities), DrainAcknowledgedThrough: through})
	}
	var attestation CNIntegrityDrainAttestation
	if err := common.Unmarshal([]byte(previous.Attestation), &attestation); err != nil {
		return nil, err
	}
	return cnPersistIntegrityFenceTx(tx, epoch.EpochID, generation, writers, attestation, previous.AttestedAt)
}
