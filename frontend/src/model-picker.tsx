import { useState } from "react";
import { Check, ChevronsUpDown } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Command,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
} from "@/components/ui/command";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover";
import type { Model } from "./api";

export function ModelPicker({
  models,
  value,
  onChange,
  id,
}: {
  models: Model[];
  value: string;
  onChange: (id: string) => void;
  id: string;
}) {
  const [open, setOpen] = useState(false);
  const selected = models.find((m) => m.id === value);
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <Button
          id={id}
          type="button"
          role="combobox"
          aria-expanded={open}
          className="model-picker"
          variant="outline"
        >
          <span>
            {selected ? selected.name || selected.id : "选择上游模型"}
          </span>
          <ChevronsUpDown />
        </Button>
      </PopoverTrigger>
      <PopoverContent className="model-popover" align="start">
        <Command>
          <CommandInput placeholder="搜索模型 ID 或展示名…" />
          <CommandList>
            <CommandEmpty>没有匹配的模型。</CommandEmpty>
            <CommandGroup>
              {models.map((m) => (
                <CommandItem
                  key={m.id}
                  value={m.id}
                  keywords={[m.name || ""]}
                  onSelect={() => {
                    onChange(m.id);
                    setOpen(false);
                  }}
                >
                  <span className="model-option">
                    <span>{m.name || m.id}</span>
                    {m.name && <small>{m.id}</small>}
                  </span>
                  {m.id === value && <Check className="ml-auto" />}
                </CommandItem>
              ))}
            </CommandGroup>
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  );
}
