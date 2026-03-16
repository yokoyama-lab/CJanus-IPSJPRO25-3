int n
int r
n += 5
call stri(n, r)

// Sequential triangular number: T(n) = n + (n-1) + ... + 1
// T(5) = 15, T(10) = 55
procedure stri(int n, int r)
    if n <= 0 then
        skip
    else
        local int l = n-1
        call stri(l, r)
        r += n
        delocal int l = n-1
    fi n <= 0
