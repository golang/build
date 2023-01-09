
rem This simple script loops forever, invoking the stage0 buildlet.
rem When stage0 runs, it will download a new copy of the main
rem buildlet and execute it.

:loop

@echo Invoking bootstrap.exe at %date% %time% on %computername%

C:\golang\bootstrap.exe

goto loop

