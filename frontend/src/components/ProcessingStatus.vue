<script setup lang="ts">
import { computed } from "vue";
import type { JobStatusName } from "@/services/api";

const props = defineProps<{
  status: JobStatusName | "idle";
  message: string;
  /** Elapsed seconds in the current active stage — shown next to the active step label. */
  elapsedStageSec?: number;
}>();

interface Step {
  key: "download" | "separate" | "melody" | "shift" | "ready";
  label: string;
}

const STEPS: Step[] = [
  { key: "download", label: "Downloading the song" },
  { key: "separate", label: "Separating vocals from music" },
  { key: "melody", label: "Reading the melody" },
  { key: "shift", label: "Tuning to your key" },
  { key: "ready", label: "Ready" },
];

type StepState = "done" | "active" | "pending";

const activeStepKey = computed<Step["key"] | null>(() => {
  switch (props.status) {
    case "queued":
    case "downloading":
      return "download";
    case "separating":
      return "separate";
    case "melody":
      return "melody";
    case "shifting":
      return "shift";
    case "done":
      return "ready";
    default:
      return null;
  }
});

const stepsWithState = computed<Array<Step & { state: StepState }>>(() => {
  const orderedKeys = STEPS.map((s) => s.key);
  const activeIdx =
    activeStepKey.value === null
      ? -1
      : orderedKeys.indexOf(activeStepKey.value);
  return STEPS.map((step, idx) => {
    let state: StepState = "pending";
    if (props.status === "done") {
      state = "done";
    } else if (activeIdx !== -1) {
      if (idx < activeIdx) state = "done";
      else if (idx === activeIdx) state = "active";
    }
    return { ...step, state };
  });
});

const isError = computed(() => props.status === "error");
const visible = computed(() => props.status !== "idle");
</script>

<template>
  <div
    v-if="visible"
    class="max-w-md mx-auto rounded-2xl p-6 bg-[var(--color-surface)] border border-[var(--color-border)]"
  >
    <div v-if="isError" class="text-[var(--color-danger)] text-sm">
      Something went wrong: {{ message || "unknown error" }}
    </div>
    <ol v-else class="space-y-5">
      <li
        v-for="step in stepsWithState"
        :key="step.key"
        class="flex items-start gap-3"
      >
        <span
          class="mt-1 w-3 h-3 rounded-full shrink-0 transition-colors"
          :class="[
            step.state === 'done'
              ? 'bg-[var(--color-success)]'
              : step.state === 'active'
                ? 'bg-[var(--color-accent)] animate-pulse'
                : 'bg-[var(--color-border)]',
          ]"
        />
        <div class="flex-1 min-w-0 flex items-baseline gap-2">
          <div
            class="text-[14px] transition-colors"
            :class="[
              step.state === 'pending'
                ? 'text-[var(--color-text-faint)]'
                : 'text-[var(--color-text)]',
            ]"
          >
            {{ step.label }}
          </div>
          <span
            v-if="
              step.state === 'active' &&
              elapsedStageSec !== undefined &&
              elapsedStageSec > 0
            "
            class="text-[12px] text-[var(--color-text-faint)] tabular-nums"
            >{{ elapsedStageSec }}s</span
          >
        </div>
      </li>
    </ol>
  </div>
</template>
