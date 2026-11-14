# ✅ Remote model installation validation

[📚 Documentation home](../../README.md)


Historical validation before the project rename, on the existing `benchmark-control` and `benchmark-node-temp` Lima VMs.
The control VM runs the PostgreSQL-backed Go controller in k3s; the target runs
the Go agent as a systemd user service.

## 🧪 Reproduce the workflow

These are historical commands. Current releases use `modelgauge apply -f benchmark.yaml`;
see the [current walkthrough](../GETTING-STARTED.md). The measurements below are retained unchanged.

Run from any CLI machine with its controller URL and admin token loaded. Replace
`JOB_ID` with the newly returned install job ID; wait for completion before
creating the campaign. Use a fresh output directory.

```sh
modelgauge model install qwen2.5-1.5b-instruct:q5_k_m --node benchmark-node-temp
modelgauge model install status JOB_ID
modelgauge model list --node benchmark-node-temp
modelgauge benchmark campaign create \
  --node benchmark-node-temp --execution-mode lima-linux \
  --models qwen2.5-1.5b-instruct:q5_k_m \
  --contexts 32 --threads 2 --submit --output q5-install-validation
modelgauge benchmark campaign status --file q5-install-validation/campaign.json
modelgauge results show --node benchmark-node-temp
```

## 📏 Recorded evidence

The historical 1,285,494,304-byte download completed and matched publisher SHA-256
`b46661073c18e5b56a41fa320975f866a00def1ff08feef4718e013258896f8c`.
The agent advertised the model on its next poll. No model or runtime paths
were supplied to the installation or campaign commands.

Plan `51ebdc49-18a8-4030-b1d6-08519ddd78d5` completed with two cases and
one memory measurement persisted to PostgreSQL:

| Measurement | Result |
|---|---:|
| Prompt, 32 tokens, 2 threads | 29.356441 tokens/s |
| Generation, 128 tokens, 2 threads | 19.890890 tokens/s |
| Peak RSS | 1,353,396,224 bytes |
| Reservation at 1.25 times RSS | 1,691,745,280 bytes |

This short run validates the download-to-results workflow. It is not a
sustained-performance baseline or evidence for longer context memory fit.
Installation status currently reports queued/running/completed/failed/cancelled;
byte-level progress is not yet exposed through the API.
