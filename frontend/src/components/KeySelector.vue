<script setup lang="ts">
import { computed } from "vue";
import { semitonesToLabel } from "@/utils/labels";

const props = defineProps<{
  semitones: number;
  disabled?: boolean;
  originalKey?: string;
  transposedKey?: string;
}>();
const emit = defineEmits<{ change: [value: number] }>();

const MIN = -12;
const MAX = 12;

const primaryLabel = computed(() => semitonesToLabel(props.semitones));

const secondaryLabel = computed(() => {
  if (!props.originalKey) return "";
  if (props.semitones === 0) return `Key: ${props.originalKey}`;
  if (!props.transposedKey) return props.originalKey;
  return `${props.originalKey} → ${props.transposedKey}`;
});

const canDec = computed(() => !props.disabled && props.semitones > MIN);
const canInc = computed(() => !props.disabled && props.semitones < MAX);

function dec() {
  if (canDec.value) emit("change", props.semitones - 1);
}
function inc() {
  if (canInc.value) emit("change", props.semitones + 1);
}
</script>

<template>
  <div class="flex flex-col items-center gap-1.5">
    <div
      class="inline-flex items-center gap-3 px-2 py-1.5 rounded-full bg-[var(--color-surface)] border border-[var(--color-border)]"
    >
      <button
        @click="dec"
        :disabled="!canDec"
        class="w-9 h-9 rounded-full flex items-center justify-center text-[var(--color-text)] text-lg hover:bg-[var(--color-surface-2)] disabled:text-[var(--color-text-faint)] disabled:hover:bg-transparent disabled:cursor-not-allowed transition-colors"
        aria-label="Lower one step"
      >
        −
      </button>
      <div
        class="min-w-[10.5rem] text-center text-[15px] font-medium text-[var(--color-text)] select-none"
      >
        {{ primaryLabel }}
      </div>
      <button
        @click="inc"
        :disabled="!canInc"
        class="w-9 h-9 rounded-full flex items-center justify-center text-[var(--color-text)] text-lg hover:bg-[var(--color-surface-2)] disabled:text-[var(--color-text-faint)] disabled:hover:bg-transparent disabled:cursor-not-allowed transition-colors"
        aria-label="Higher one step"
      >
        +
      </button>
    </div>
    <div
      v-if="secondaryLabel"
      class="serif-italic text-[14px] text-[var(--color-text-muted)] tracking-wide"
    >
      {{ secondaryLabel }}
    </div>
  </div>
</template>
