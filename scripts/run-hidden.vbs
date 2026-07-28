Option Explicit

Dim arguments, command, index, shell, exitCode
Set arguments = WScript.Arguments

If arguments.Count < 1 Then
    WScript.Quit 87
End If

command = QuoteArgument(arguments(0))
For index = 1 To arguments.Count - 1
    command = command & " " & QuoteArgument(arguments(index))
Next

Set shell = CreateObject("WScript.Shell")
exitCode = shell.Run(command, 0, True)
WScript.Quit exitCode

Function QuoteArgument(value)
    QuoteArgument = Chr(34) & Replace(value, Chr(34), "\" & Chr(34)) & Chr(34)
End Function
