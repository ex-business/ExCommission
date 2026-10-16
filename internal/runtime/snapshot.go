package runtime

import calc "github.com/ex-business/ExCommission/internal/calculation/domain"

// publicSnapshot exposes the frozen business contract without coupling clients to Go field names.
func publicSnapshot(input calc.Input) map[string]any {
	qualifications := map[string]any{}
	for id, q := range input.QualificationFacts {
		qualifications[id] = q
	}
	contracts := map[string]any{}
	for id, c := range input.Contracts {
		if c.From.IsZero() {
			contracts[id] = map[string]string{"status": "ABSENT"}
		} else {
			v := map[string]any{"status": "PRESENT", "plan_id": c.PlanID, "effective_from": c.From, "effective_to": c.To}
			if c.ID != "" {
				v["contract_id"] = c.ID
			}
			if c.Evidence != "" {
				v["evidence_ref"] = c.Evidence
			}
			contracts[id] = v
		}
	}
	recipients := map[string]any{}
	for _, a := range input.Recipients {
		recipients[a.ID] = map[string]any{"agent_id": a.ID, "eligible": a.Eligible, "status": a.Status, "depth": a.Depth}
	}
	relationships := []map[string]any{}
	for _, r := range input.RelationshipFacts {
		v := map[string]any{"agent_id": r.AgentID, "depth": r.Depth, "effective_from": r.From, "effective_to": r.To, "kind": r.Kind}
		if r.Evidence != "" {
			v["evidence_ref"] = r.Evidence
		}
		relationships = append(relationships, v)
	}
	algorithms := input.AlgorithmVersions
	if algorithms == nil {
		algorithms = map[string]string{}
		for _, r := range input.Plan.Rules {
			algorithms[r.Algorithm] = r.Version
		}
	}
	result := map[string]any{"snapshot_id": input.SnapshotID, "standard_version": input.StandardVersion, "plan_id": input.Plan.PlanID, "plan_version_id": input.PlanVersionID, "config_hash": input.ConfigHash, "business_time": input.Facts.BusinessTime, "currency_catalog_version": input.CurrencyCatalogVersion, "calendar_version": input.CalendarVersion, "facts": input.Facts, "ancestors": input.Ancestors, "qualification_facts": qualifications, "contract_facts": contracts, "recipient_facts": recipients, "relationship_facts": relationships, "algorithm_versions": algorithms}
	if len(input.TriggerEvidence) > 0 {
		result["trigger_evidence"] = input.TriggerEvidence
	}
	return result
}
