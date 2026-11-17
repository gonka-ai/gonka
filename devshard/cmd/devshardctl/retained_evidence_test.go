package main

import "devshard/gatewayproto"

func retainedErrorStream(lines []string, complete, truncated bool) *gatewayproto.EvidenceCollector {
	retained := gatewayproto.NewEvidenceCollector(maxErrorStreamBytes, nil, nil)
	retained.AppendLines(lines)
	if complete && !retained.Complete() {
		retained.AppendLines([]string{"data: [DONE]"})
	}
	if truncated {
		retained.MarkTruncated()
	}
	return retained
}
